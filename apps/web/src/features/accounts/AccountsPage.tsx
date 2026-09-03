import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import type {
  KeyboardEvent,
  MouseEvent as ReactMouseEvent,
  ReactNode,
  SetStateAction,
} from 'react';
import { createPortal } from 'react-dom';
import { useLocation, useNavigate, type BlockerFunction } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Drawer } from '@/components/ui/Drawer';
import { DropdownMenu, type DropdownMenuItem } from '@/components/ui/DropdownMenu';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { Modal } from '@/components/ui/Modal';
import { Select } from '@/components/ui/Select';
import { SegmentedTabs, type SegmentedTabItem } from '@/components/ui/SegmentedTabs';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import {
  IconBinary,
  IconCheck,
  IconArrowDownWideNarrow,
  IconArrowUpNarrowWide,
  IconCopy,
  IconDollarSign,
  IconDownload,
  IconEye,
  IconEyeOff,
  IconFileText,
  IconKey,
  IconMoreVertical,
  IconModelCluster,
  IconPlus,
  IconRefreshCw,
  IconSearch,
  IconSend,
  IconSettings,
  IconShield,
  IconSlidersHorizontal,
  IconTrash2,
  IconX,
} from '@/components/ui/icons';
import {
  ANTIGRAVITY_CONFIG,
  CLAUDE_CONFIG,
  CODEX_CONFIG,
  KIMI_CONFIG,
  XAI_CONFIG,
  buildObservedCodexQuotaState,
  refreshQuotaWithConfig,
  type QuotaConfig,
  type QuotaSetter,
} from '@/components/quota';
import { buildQuotaFailureState, getScopedQuotaState } from '@/components/quota/quotaConfigs';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useInterval } from '@/hooks/useInterval';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { useUnsavedChangesGuard } from '@/hooks/useUnsavedChangesGuard';
import { getAuthFileIcon } from '@/features/authFiles/constants';
import {
  useAuthFilesData,
  type AuthFilesCredentialMutation,
} from '@/features/authFiles/hooks/useAuthFilesData';
import { useAuthFilesOauth } from '@/features/authFiles/hooks/useAuthFilesOauth';
import { useAuthFilesModels } from '@/features/authFiles/hooks/useAuthFilesModels';
import { useAuthFileConfigurationEditor } from '@/features/authFiles/hooks/useAuthFileConfigurationEditor';
import {
  createCredentialInspectionSnapshotScopeKey,
  useCredentialInspectionSnapshot,
} from '@/features/accounts/hooks/useCredentialInspectionSnapshot';
import { useAccountsWorkspaceRefresh } from '@/features/accounts/hooks/useAccountsWorkspaceRefresh';
import { useHeaderSnapshotsLoader } from '@/features/monitoring/hooks/useHeaderSnapshotsLoader';
import { PaginationControls } from '@/features/monitoring/components/MonitoringShared';
import { CredentialHealthInspectionWorkspace } from '@/features/monitoring/components/CredentialHealthInspectionWorkspace';
import { AuthJsonPasteModal } from '@/features/authFiles/components/AuthJsonPasteModal';
import { OAuthExcludedCard } from '@/features/authFiles/components/OAuthExcludedCard';
import {
  OAuthExcludedEditorModal,
  OAuthModelAliasEditorModal,
} from '@/features/authFiles/components/OAuthEditorModals';
import { OAuthModelAliasCard } from '@/features/authFiles/components/OAuthModelAliasCard';
import { CodexReauthDialog } from '@/features/oauth/CodexReauthDialog';
import {
  CodexReauthReconciliationError,
  createCodexReauthTargetFromAuthFile,
  type CodexReauthTarget,
} from '@/features/oauth/codexReauthModel';
import { runCredentialVisibilityRetry } from '@/features/accounts/model/accountCredentialVisibilityRetry';
import {
  ACCOUNT_CODEX_STATUS_FILTERS,
  buildAccountInspectionBySelectionKey,
  buildAccountMetrics,
  buildAccountRows,
  filterSuppressedAccountInspectionResults,
  findAccountRowForInspectionTarget,
  filterAccountRows,
  getHandledAccountInspectionResultKeys,
  getPlanOptionLabel,
  getPlanOptionValue,
  getPlanOptions,
  getProviderOptions,
  isAccountCodexStatusFilter,
  normalizeAccountProvider,
  readAuthFileCredentialRefreshAtMs,
  readAuthFileUpdatedAtMs,
  sortAccountRows,
  type AccountQuotaBand,
  type AccountRow,
  type AccountRowSort,
  type AccountStatusFilter,
} from '@/features/accounts/model/accountRows';
import {
  buildInspectionCodexQuotaState,
  getEffectiveAccountInspectionAction,
  getAccountCredentialEvidenceCutoffs,
  getCodexQuotaEvidenceAtMs,
  isEvidenceOlderThan,
  isKnownHealthyCodexQuota,
  mergeConfirmedReauthCodexQuotaStates,
  reconcileCodexQuotaEvidence,
  stripSupersededAccountInspectionStatus,
  type AccountCredentialEvidenceBoundary,
} from '@/features/accounts/model/accountCredentialEvidence';
import {
  loadAccountCredentialEvidenceBoundaryState,
  saveAccountCredentialEvidenceBoundaryState,
} from '@/features/accounts/model/accountCredentialEvidenceStorage';
import {
  acknowledgeAccountCredentialMutationMarkers,
  createAccountCredentialMutationBaseline,
  hasAccountCredentialMutationEvidence,
  listAccountCredentialMutationMarkers,
  resolveAccountCredentialMutationFiles,
  type AccountCredentialMutationMarker,
} from '@/features/accounts/model/accountCredentialMutationMarker';
import {
  acknowledgePendingAccountDirectReauths,
  createAccountDirectReauthBaseline,
  listPendingAccountDirectReauths,
  reconcileAccountDirectReauth,
  recordPendingAccountDirectReauth,
  type AccountDirectReauthBaseline,
  type AccountDirectReauthReconciliation,
  type PendingAccountDirectReauth,
} from '@/features/accounts/model/accountDirectReauth';
import { buildAccountRecommendations } from '@/features/accounts/model/quotaRecommendations';
import {
  buildAccountListItem,
  buildRecommendationBySelectionKey,
  type AccountListHealthStatusKey,
} from '@/features/accounts/model/accountListPresentation';
import {
  buildAccountHistoryByRowKey,
  buildAccountHistoryTargetBatches,
  buildAccountHistoryTargetEntries,
  retainAccountHistoryRowKeys,
  type AccountHistoryTargetEntry,
} from '@/features/accounts/model/accountHistoryRows';
import { mapWithConcurrency } from '@/features/accounts/model/asyncPool';
import {
  buildAccountWindowUsageByKey,
  buildAccountWindowUsageTargetEntries,
  filterAccountWindowUsageByTargetRanges,
} from '@/features/accounts/model/accountWindowUsageRows';
import {
  buildAccountQuotaDisplayWindows,
  getQuotaWindowShortLabel,
  isStandardAccountQuotaListWindow,
  type AccountQuotaDisplayWindow,
} from '@/features/accounts/model/accountQuotaDisplayWindows';
import {
  buildAccountQuotaWindowDefinitions,
  type AccountQuotaWindowDefinition,
} from '@/features/accounts/model/accountQuotaWindowDefinitions';
import {
  buildAccountQuotaSnapshotQueryAccounts,
  buildAccountQuotaSnapshotWriteEntries,
  mergeCodexResetCreditsFromQuotaSnapshots,
  mergeAccountQuotaSnapshotWindows,
} from '@/features/accounts/model/accountQuotaSnapshots';
import {
  ACCOUNT_OVERVIEW_ACTIVITY_RANGE_MS,
  buildAccountDetailViewModel,
} from '@/features/accounts/model/accountDetailViewModel';
import {
  ACCOUNT_SORT_DEFAULT_DIRECTIONS,
  ACCOUNT_SORT_FIELD_OPTIONS,
  DETAIL_EVENTS_LIMIT,
  DETAIL_EVENTS_RANGE_MS,
  PAGE_SIZE_OPTIONS,
  buildAntigravityQuotaMatrix,
  formatHistoryNumber,
  formatHistorySuccessRate,
  formatPercent,
  formatQuotaResetDisplay,
  formatQuotaResetTooltipParams,
  getAccountQuotaLifecycleBarOverride,
  getAccountQuotaFallbackVisibleScopeLabel,
  getAccountHistoryTitle,
  getAccountSortFieldOption,
  getProviderLabel,
  parsePriorityValue,
  selectAccountQuotaListWindows,
  toAuthFileCodexInspectionSnapshot,
  type AccountSortFieldValue,
  type AccountQuotaLifecycleBarOverride,
  type AccountsView,
  type DetailTab,
} from '@/features/accounts/model/accountsPagePresentation';
import { formatCompactNumber, formatCompactUsd, formatUsd } from '@/utils/usage';
import {
  getAuthFileCodexInspectionKeyForFile,
  getAuthFileCodexInspectionKeyForIdentity,
  getAuthFileNameFromSelectionKey,
  getAuthFileCodexStatus,
  getAuthFilePatchTarget,
  getAuthFileSelectionKey,
  getAuthFileCredentialStatusCodes,
  getAuthFileScopedCodexQuota,
  getFreshAuthFileCodexStatusSources,
  hasPartialSharedAuthFileSelection,
  sanitizeSupersededAuthQuotaState,
  sanitizeSupersededAuthHeaderSnapshot,
  isObservedCodexAuthenticationError,
} from '@/features/authFiles/model/credentialStatus';
import {
  buildUsageValueRowFromMonitoringSummary,
  buildUsageValueRowsFromRecent,
  type UsageValueRow,
} from '@/features/accounts/model/usageValueRows';
import {
  buildOAuthRulePreviewRows,
  getOAuthRulePreviewProviders,
  partitionOAuthRulePreviewRows,
} from '@/features/accounts/model/oauthRulePreview';
import { resolveAccountReauthAction } from '@/features/accounts/model/accountReauth';
import {
  beginAccountOAuthReauthSession,
  buildAccountOAuthReauthPath,
  readCompletedAccountReauthResultKeys,
  recordCompletedAccountReauthSession,
} from '@/features/accounts/model/accountReauthSession';
import { beginAccountQuotaRequest } from '@/features/accounts/model/accountQuotaRequestGate';
import {
  buildAccountOperationalItemsByRowKey,
  getAccountOperationalItemIdentityKey,
} from '@/features/accounts/model/accountOperationalScope';
import {
  getAccountRequestCredentialEvidence,
  isAccountInspectionStatusEvidenceCurrent,
  isAccountRequestCredentialEvidenceCurrent,
  resolveAccountAuthenticationProblemEvidence,
  resolveAccountRequestHealthEvidence,
  type AccountRequestEvidenceInput,
} from '@/features/accounts/model/accountHealthEvidence';
import {
  DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE,
  readAccountsWorkspaceUiState,
  writeAccountsWorkspaceUiState,
  type AccountOperationalFilter,
  type AccountsWorkspaceUiState,
} from '@/features/accounts/model/accountsWorkspaceUiState';
import {
  readAccountsWorkspaceUrlState,
  writeAccountsWorkspaceUrlSearch,
} from '@/features/accounts/model/accountsWorkspaceUrlState';
import {
  AccountConfigurationTab,
  AccountDiagnosticsTab,
  AccountLatestRequest,
  AccountMetricsGrid,
  AccountModelsTab,
  AccountOverviewTab,
  AccountProviderTabs,
  AccountQuotaMatrix,
  AccountQuotaTab,
  AccountsBatchDeletePreview,
} from '@/features/accounts/components';
import {
  accountQuotaSnapshotApi,
  consumeCodexRateLimitResetCredit,
  monitoringAnalyticsApi,
  usageServiceApi,
  type AccountActionCandidate,
  type AccountQuotaSnapshotObservationInput,
  type AccountQuotaSnapshotWriteEntry,
  type AccountQuotaSnapshotWindow,
  type MonitoringAnalyticsAccountStatRow,
  type MonitoringAnalyticsEventRow,
  type MonitoringAnalyticsRecentFailure,
  type MonitoringAnalyticsSummary,
  type MonitoringAccountHistoryItem,
  type MonitoringAccountWindowUsageItem,
  type QuotaCooldownInfo,
  type UsageHeaderSnapshot,
  type UsageHeaderSnapshotsResponse,
} from '@/services/api';
import type { AuthFileItem, CodexQuotaState, XaiQuotaState } from '@/types';
import type { CodexQuotaData } from '@/utils/quota';
import type { AuthJsonInputType } from '@/features/authFiles/sessionAuthConverter';
import {
  maskQuotaAccountText,
  type QuotaAccountDisplayMode,
} from '@/components/quota/quotaDisplay';
import {
  captureQuotaCacheGeneration,
  commitIfQuotaCacheCurrent,
  publishAccountCredentialMutationRevision,
  useAuthStore,
  useNotificationStore,
  useQuotaStore,
  useThemeStore,
} from '@/stores';
import { useUsageHeaderSnapshotStore } from '@/stores/useUsageHeaderSnapshotStore';
import { normalizeAuthIndex } from '@/utils/authIndex';
import { copyToClipboard } from '@/utils/clipboard';
import {
  buildUsageHeaderSnapshotLookup,
  getHeaderSnapshotErrorCode,
  getHeaderSnapshotErrorKind,
  getHighConfidenceUsageHeaderSnapshotForAuthFile,
  hasUsageHeaderQuotaSignal,
  isUsageHeaderQuotaSnapshotExpired,
} from '@/utils/usageHeaderSnapshots';
import {
  buildQuotaCredentialIdentity,
  getCredentialScopedQuotaState,
  getQuotaCredentialStoreKey,
} from '@/utils/quota/credentialScope';
import {
  buildProviderCredentialTaskPlan,
  runProviderCredentialTaskPlan,
} from '@/utils/quota/providerRefreshScheduler';
import { createCodexInspectionConnectionFingerprint } from '@/features/monitoring/codexInspection';
import type {
  CredentialHealthInspectionMode,
  CredentialInspectionSnapshot,
  CredentialInspectionTarget,
} from '@/features/monitoring/model/credentialInspectionSnapshot';
import { getServerCredentialMutationSyncKey } from '@/features/monitoring/model/credentialInspectionSnapshot';
import styles from './AccountsPage.module.scss';

const renderAccountDetailTrigger = ({
  isSelectionMode,
  className,
  title,
  ariaLabel,
  kind,
  onOpen,
  children,
}: {
  isSelectionMode: boolean;
  className: string;
  title: string;
  ariaLabel: string;
  kind: 'history' | 'quota';
  onOpen: () => void;
  children: ReactNode;
}) =>
  isSelectionMode ? (
    <div className={className} title={title} data-account-detail-region={kind}>
      {children}
    </div>
  ) : (
    <button
      type="button"
      className={`${className} ${styles.accountCardDetailTrigger}`}
      title={title}
      aria-label={ariaLabel}
      data-account-detail-region={kind}
      data-account-detail-trigger={kind}
      onClick={onOpen}
    >
      {children}
    </button>
  );

const MAX_CONCURRENT_QUOTA_REFRESHES_PER_PROVIDER = 1;
const MAX_CONCURRENT_QUOTA_REFRESH_PROVIDERS = 3;
const MAX_CONCURRENT_ACCOUNT_HISTORY_REQUESTS = 2;
const PASSIVE_ACCOUNTS_EVIDENCE_REFRESH_MS = 60_000;
const CREDENTIAL_EVIDENCE_UNIQUE_FILE_NAME_BOUNDARY_PREFIX = 'unique-file-name\u0000';
const CREDENTIAL_EVIDENCE_SOURCE_FILE_BOUNDARY_PREFIX = 'source-file\u0000';
const CREDENTIAL_EVIDENCE_PROVIDER_BOUNDARY_PREFIX = 'provider\u0000';
interface CodexCredentialEvidenceInvalidation {
  file: AuthFileItem;
  invalidatedAtMs: number;
}

interface AccountDirectReauthReconciliationBatch {
  files: AuthFileItem[];
  reconciliations: Map<string, AccountDirectReauthReconciliation>;
}

interface AccountCredentialEvidenceBoundarySessionState {
  scopeKey: string;
  evidence: Map<string, AccountCredentialEvidenceBoundary>;
  status: Map<string, AccountCredentialEvidenceBoundary>;
}

const EMPTY_ACCOUNT_CREDENTIAL_EVIDENCE_BOUNDARY: AccountCredentialEvidenceBoundary = {
  localAtMs: 0,
  inspectionAtMs: 0,
  inspectionBaselinePending: false,
  headerAtMs: 0,
  headerBaselinePending: false,
  actionAtMs: 0,
  actionBaselinePending: false,
  authenticationActionAtMs: 0,
  authenticationActionBaselinePending: false,
  quotaActionAtMs: 0,
  quotaActionBaselinePending: false,
  cooldownAtMs: 0,
  cooldownBaselinePending: false,
  fallbackInspectionAtMs: 0,
  fallbackInspectionBaselinePending: false,
  fallbackHeaderAtMs: 0,
  fallbackHeaderBaselinePending: false,
  fallbackActionAtMs: 0,
  fallbackActionBaselinePending: false,
  fallbackCooldownAtMs: 0,
  fallbackCooldownBaselinePending: false,
  authenticationAtMs: 0,
  rawStatusAtMs: 0,
  rawStatusMessages: [],
  rawStatusCodes: [],
};

const mirrorAccountCredentialEvidenceBoundaryToFallback = (
  boundary: AccountCredentialEvidenceBoundary
): AccountCredentialEvidenceBoundary => ({
  ...boundary,
  fallbackInspectionAtMs: Math.max(boundary.fallbackInspectionAtMs, boundary.inspectionAtMs),
  fallbackInspectionBaselinePending:
    boundary.fallbackInspectionBaselinePending === true ||
    boundary.inspectionBaselinePending === true,
  fallbackHeaderAtMs: Math.max(boundary.fallbackHeaderAtMs, boundary.headerAtMs),
  fallbackHeaderBaselinePending:
    boundary.fallbackHeaderBaselinePending === true || boundary.headerBaselinePending === true,
  fallbackActionAtMs: Math.max(boundary.fallbackActionAtMs, boundary.actionAtMs),
  fallbackActionBaselinePending:
    boundary.fallbackActionBaselinePending === true || boundary.actionBaselinePending === true,
  fallbackCooldownAtMs: Math.max(boundary.fallbackCooldownAtMs, boundary.cooldownAtMs),
  fallbackCooldownBaselinePending:
    boundary.fallbackCooldownBaselinePending === true || boundary.cooldownBaselinePending === true,
  authenticationAtMs: boundary.authenticationAtMs,
  rawStatusAtMs: boundary.rawStatusAtMs,
  rawStatusMessages: boundary.rawStatusMessages,
  rawStatusCodes: boundary.rawStatusCodes,
});

const toFallbackAccountCredentialEvidenceBoundary = (
  boundary: AccountCredentialEvidenceBoundary
): AccountCredentialEvidenceBoundary => ({
  ...EMPTY_ACCOUNT_CREDENTIAL_EVIDENCE_BOUNDARY,
  localAtMs: boundary.localAtMs,
  fallbackInspectionAtMs: Math.max(boundary.fallbackInspectionAtMs, boundary.inspectionAtMs),
  fallbackInspectionBaselinePending:
    boundary.fallbackInspectionBaselinePending === true ||
    boundary.inspectionBaselinePending === true,
  fallbackHeaderAtMs: Math.max(boundary.fallbackHeaderAtMs, boundary.headerAtMs),
  fallbackHeaderBaselinePending:
    boundary.fallbackHeaderBaselinePending === true || boundary.headerBaselinePending === true,
  fallbackActionAtMs: Math.max(boundary.fallbackActionAtMs, boundary.actionAtMs),
  fallbackActionBaselinePending:
    boundary.fallbackActionBaselinePending === true || boundary.actionBaselinePending === true,
  fallbackCooldownAtMs: Math.max(boundary.fallbackCooldownAtMs, boundary.cooldownAtMs),
  fallbackCooldownBaselinePending:
    boundary.fallbackCooldownBaselinePending === true || boundary.cooldownBaselinePending === true,
  authenticationAtMs: boundary.authenticationAtMs,
  rawStatusAtMs: boundary.rawStatusAtMs,
  rawStatusMessages: boundary.rawStatusMessages,
  rawStatusCodes: boundary.rawStatusCodes,
});

const getCredentialEvidenceUniqueFileNameBoundaryKey = (fileName: string): string =>
  `${CREDENTIAL_EVIDENCE_UNIQUE_FILE_NAME_BOUNDARY_PREFIX}${fileName}`;

const getCredentialEvidenceSourceFileBoundaryKey = (fileName: string): string =>
  `${CREDENTIAL_EVIDENCE_SOURCE_FILE_BOUNDARY_PREFIX}${fileName}`;

const getCredentialEvidenceProviderBoundaryKey = (provider: string): string =>
  `${CREDENTIAL_EVIDENCE_PROVIDER_BOUNDARY_PREFIX}${provider}`;

const clearCredentialSpecificFallbackEvidence = (
  boundary: AccountCredentialEvidenceBoundary
): AccountCredentialEvidenceBoundary => ({
  ...boundary,
  authenticationAtMs: 0,
  rawStatusAtMs: 0,
  rawStatusMessages: [],
  rawStatusCodes: [],
});

const keepAuthenticationRecoveryBoundary = (
  boundary: AccountCredentialEvidenceBoundary
): AccountCredentialEvidenceBoundary => ({
  ...boundary,
  inspectionAtMs: 0,
  inspectionBaselinePending: false,
  headerAtMs: 0,
  headerBaselinePending: false,
  fallbackInspectionAtMs: 0,
  fallbackInspectionBaselinePending: false,
  fallbackHeaderAtMs: 0,
  fallbackHeaderBaselinePending: false,
});

const releaseObservedRawStatusBoundaries = (
  current: Map<string, AccountCredentialEvidenceBoundary>,
  files: readonly AuthFileItem[]
): Map<string, AccountCredentialEvidenceBoundary> => {
  let next: Map<string, AccountCredentialEvidenceBoundary> | null = null;
  current.forEach((boundary, key) => {
    const boundaryRawStatusMessages = boundary.rawStatusMessages ?? [];
    const boundaryRawStatusCodes = boundary.rawStatusCodes ?? [];
    const uniqueFileNameFallback = key.startsWith(
      CREDENTIAL_EVIDENCE_UNIQUE_FILE_NAME_BOUNDARY_PREFIX
    );
    let matchingFiles: readonly AuthFileItem[];
    if (key.startsWith(CREDENTIAL_EVIDENCE_SOURCE_FILE_BOUNDARY_PREFIX)) {
      const fileName = key.slice(CREDENTIAL_EVIDENCE_SOURCE_FILE_BOUNDARY_PREFIX.length);
      matchingFiles = files.filter((file) => file.name === fileName);
    } else if (key.startsWith(CREDENTIAL_EVIDENCE_PROVIDER_BOUNDARY_PREFIX)) {
      const provider = key.slice(CREDENTIAL_EVIDENCE_PROVIDER_BOUNDARY_PREFIX.length);
      matchingFiles = files.filter((file) => normalizeAccountProvider(file) === provider);
    } else if (uniqueFileNameFallback) {
      const fileName = key.slice(CREDENTIAL_EVIDENCE_UNIQUE_FILE_NAME_BOUNDARY_PREFIX.length);
      matchingFiles = files.filter((file) => file.name === fileName);
    } else {
      matchingFiles = files.filter((file) => getAuthFileSelectionKey(file) === key);
    }
    const hasCredentialSpecificFallbackEvidence =
      boundary.authenticationAtMs > 0 ||
      boundary.rawStatusAtMs > 0 ||
      boundaryRawStatusMessages.length > 0 ||
      boundaryRawStatusCodes.length > 0;
    if (matchingFiles.length === 0) {
      if (!uniqueFileNameFallback || !hasCredentialSpecificFallbackEvidence) return;
      if (!next) next = new Map(current);
      next.set(key, clearCredentialSpecificFallbackEvidence(boundary));
      return;
    }
    const sharedUniqueFileNameFallback = uniqueFileNameFallback && matchingFiles.length > 1;
    const boundaryForRelease = sharedUniqueFileNameFallback
      ? clearCredentialSpecificFallbackEvidence(boundary)
      : boundary;
    const shouldReleaseCredentialSpecificFallback =
      sharedUniqueFileNameFallback &&
      (boundaryForRelease.authenticationAtMs !== boundary.authenticationAtMs ||
        boundaryForRelease.rawStatusAtMs !== boundary.rawStatusAtMs ||
        boundaryForRelease.rawStatusMessages.length !== boundaryRawStatusMessages.length ||
        boundaryForRelease.rawStatusCodes.length !== boundaryRawStatusCodes.length);
    if (
      boundaryRawStatusMessages.length === 0 &&
      boundaryRawStatusCodes.length === 0 &&
      !shouldReleaseCredentialSpecificFallback
    ) {
      return;
    }
    const currentMessages = new Set(matchingFiles.map(readAccountRawStatusMessage).filter(Boolean));
    const currentStatusCodes = new Set(
      matchingFiles.flatMap((file) => getAuthFileCredentialStatusCodes(file))
    );
    const remainingMessages = sharedUniqueFileNameFallback
      ? []
      : boundaryRawStatusMessages.filter((message) => currentMessages.has(message));
    const remainingStatusCodes = sharedUniqueFileNameFallback
      ? []
      : boundaryRawStatusCodes.filter((statusCode) => currentStatusCodes.has(statusCode));
    if (
      !shouldReleaseCredentialSpecificFallback &&
      remainingMessages.length === boundaryRawStatusMessages.length &&
      remainingStatusCodes.length === boundaryRawStatusCodes.length
    ) {
      return;
    }
    if (!next) next = new Map(current);
    next.set(key, {
      ...boundaryForRelease,
      rawStatusAtMs:
        remainingMessages.length > 0 || remainingStatusCodes.length > 0
          ? boundaryForRelease.rawStatusAtMs
          : 0,
      rawStatusMessages: remainingMessages,
      rawStatusCodes: remainingStatusCodes,
    });
  });
  return next ?? current;
};

const pruneCodexQuotaStatesForCredentialMutation = (
  current: Record<string, CodexQuotaState>,
  {
    affectedFileNames,
    invalidatedStoreKeys = new Set<string>(),
    preservedFiles = [],
    preserveEvidenceAfterMs = 0,
  }: {
    affectedFileNames: ReadonlySet<string>;
    invalidatedStoreKeys?: ReadonlySet<string>;
    preservedFiles?: readonly AuthFileItem[];
    preserveEvidenceAfterMs?: number;
  }
): Record<string, CodexQuotaState> => {
  let changed = false;
  const next = { ...current };
  Object.entries(current).forEach(([key, state]) => {
    if (
      preserveEvidenceAfterMs > 0 &&
      (getCodexQuotaEvidenceAtMs(state) ?? 0) > preserveEvidenceAfterMs
    ) {
      return;
    }
    if (invalidatedStoreKeys.has(key)) {
      delete next[key];
      changed = true;
      return;
    }
    const stateFileName = state.authFileName?.trim() ?? '';
    if (!affectedFileNames.has(key) && !affectedFileNames.has(stateFileName)) return;
    if (preservedFiles.some((file) => getAuthFileScopedCodexQuota(file, state) === state)) return;
    delete next[key];
    changed = true;
  });
  return changed ? next : current;
};

const getBaselineCodexQuotaStoreKey = (baseline: AccountDirectReauthBaseline): string =>
  getQuotaCredentialStoreKey({
    name: baseline.target.fileName ?? '',
    type: 'codex',
    provider: baseline.target.provider ?? 'codex',
    id: baseline.target.runtimeId ?? undefined,
    authIndex: baseline.target.authIndex ?? null,
    account_id: baseline.target.accountId ?? undefined,
    accountSnapshot: baseline.target.accountSnapshot ?? undefined,
    account: baseline.target.account,
  });

const migrateConfirmedReauthCodexQuotaState = (
  current: Record<string, CodexQuotaState>,
  {
    baseline,
    confirmedFile,
    inventoryFiles,
    authenticationAtMs,
  }: {
    baseline: AccountDirectReauthBaseline;
    confirmedFile: AuthFileItem;
    inventoryFiles: readonly AuthFileItem[];
    authenticationAtMs: number;
  }
): Record<string, CodexQuotaState> => {
  const oldStoreKey = getBaselineCodexQuotaStoreKey(baseline);
  const newStoreKey = getQuotaCredentialStoreKey(confirmedFile);
  if (!oldStoreKey || oldStoreKey === newStoreKey || authenticationAtMs <= 0) return current;

  // If the old exact key is still owned by a different post-OAuth credential,
  // the state cannot be attributed safely to the confirmed credential.
  if (
    inventoryFiles.some(
      (file) =>
        getQuotaCredentialStoreKey(file) === oldStoreKey &&
        getQuotaCredentialStoreKey(file) !== newStoreKey
    )
  ) {
    return current;
  }

  const oldEntries = Object.entries(current).filter(
    ([, state]) => state.authFileKey?.trim() === oldStoreKey
  );
  if (oldEntries.length === 0) return current;

  const sourceEntry = [...oldEntries].sort(([leftKey, leftState], [rightKey, rightState]) => {
    const leftAtMs = getCodexQuotaEvidenceAtMs(leftState) ?? 0;
    const rightAtMs = getCodexQuotaEvidenceAtMs(rightState) ?? 0;
    return (
      rightAtMs - leftAtMs ||
      Number(rightKey === oldStoreKey) - Number(leftKey === oldStoreKey) ||
      rightState.windows.length - leftState.windows.length
    );
  })[0];
  if (!sourceEntry) return current;
  const [, sourceState] = sourceEntry;
  const existingNewState = current[newStoreKey];
  const mergedState = mergeConfirmedReauthCodexQuotaStates(
    sourceState,
    existingNewState?.authFileKey?.trim() === newStoreKey ? existingNewState : undefined,
    authenticationAtMs
  );
  if (!mergedState) return current;
  const migratedState: CodexQuotaState = {
    ...mergedState,
    ...buildQuotaCredentialIdentity(confirmedFile),
  };

  const next = { ...current };
  oldEntries.forEach(([key]) => {
    if (key !== newStoreKey) delete next[key];
  });
  next[newStoreKey] = migratedState;
  return next;
};

const pruneCredentialQuotaStatesForProviderMutation = <
  TState extends {
    authFileName?: string;
    authFileKey?: string;
    fetchedAtMs?: number;
    failedAtMs?: number;
  },
>(
  current: Record<string, TState>,
  targetFiles: readonly AuthFileItem[],
  getStoreKey: (file: AuthFileItem) => string,
  preserveEvidenceAfterMs = 0,
  preservedFiles: readonly AuthFileItem[] = []
): Record<string, TState> => {
  const storeKeys = new Set(targetFiles.map(getStoreKey));
  const fileNames = new Set(targetFiles.map((file) => file.name));
  let changed = false;
  const next = { ...current };
  Object.entries(current).forEach(([key, state]) => {
    const evidenceAtMs = Math.max(state.fetchedAtMs ?? 0, state.failedAtMs ?? 0);
    if (preserveEvidenceAfterMs > 0 && evidenceAtMs > preserveEvidenceAfterMs) return;
    const stateFileName = state.authFileName?.trim() ?? '';
    const stateFileKey = state.authFileKey?.trim() ?? '';
    if (
      !storeKeys.has(key) &&
      (!stateFileKey || !storeKeys.has(stateFileKey)) &&
      (!stateFileName || !fileNames.has(stateFileName))
    ) {
      return;
    }
    if (
      preservedFiles.some((file) => {
        const preservedStoreKey = getStoreKey(file);
        return key === preservedStoreKey || stateFileKey === preservedStoreKey;
      })
    ) {
      return;
    }
    delete next[key];
    changed = true;
  });
  return changed ? next : current;
};

const mergeAccountCredentialEvidenceBoundaries = (
  ...boundaries: Array<AccountCredentialEvidenceBoundary | undefined>
): AccountCredentialEvidenceBoundary => {
  const rawStatusMessages = new Set<string>();
  const rawStatusCodes = new Set<number>();
  let localAtMs = 0;
  let inspectionAtMs = 0;
  let inspectionBaselinePending = false;
  let headerAtMs = 0;
  let headerBaselinePending = false;
  let actionAtMs = 0;
  let actionBaselinePending = false;
  let authenticationActionAtMs = 0;
  let authenticationActionBaselinePending = false;
  let quotaActionAtMs = 0;
  let quotaActionBaselinePending = false;
  let cooldownAtMs = 0;
  let cooldownBaselinePending = false;
  let fallbackInspectionAtMs = 0;
  let fallbackInspectionBaselinePending = false;
  let fallbackHeaderAtMs = 0;
  let fallbackHeaderBaselinePending = false;
  let fallbackActionAtMs = 0;
  let fallbackActionBaselinePending = false;
  let fallbackCooldownAtMs = 0;
  let fallbackCooldownBaselinePending = false;
  let authenticationAtMs = 0;
  let rawStatusAtMs = 0;
  boundaries.forEach((boundary) => {
    if (!boundary) return;
    localAtMs = Math.max(localAtMs, boundary.localAtMs);
    inspectionAtMs = Math.max(inspectionAtMs, boundary.inspectionAtMs);
    inspectionBaselinePending =
      inspectionBaselinePending || boundary.inspectionBaselinePending === true;
    headerAtMs = Math.max(headerAtMs, boundary.headerAtMs);
    headerBaselinePending = headerBaselinePending || boundary.headerBaselinePending === true;
    actionAtMs = Math.max(actionAtMs, boundary.actionAtMs);
    actionBaselinePending = actionBaselinePending || boundary.actionBaselinePending === true;
    authenticationActionAtMs = Math.max(
      authenticationActionAtMs,
      boundary.authenticationActionAtMs
    );
    authenticationActionBaselinePending =
      authenticationActionBaselinePending || boundary.authenticationActionBaselinePending === true;
    quotaActionAtMs = Math.max(quotaActionAtMs, boundary.quotaActionAtMs);
    quotaActionBaselinePending =
      quotaActionBaselinePending || boundary.quotaActionBaselinePending === true;
    cooldownAtMs = Math.max(cooldownAtMs, boundary.cooldownAtMs);
    cooldownBaselinePending = cooldownBaselinePending || boundary.cooldownBaselinePending === true;
    fallbackInspectionAtMs = Math.max(fallbackInspectionAtMs, boundary.fallbackInspectionAtMs);
    fallbackInspectionBaselinePending =
      fallbackInspectionBaselinePending || boundary.fallbackInspectionBaselinePending === true;
    fallbackHeaderAtMs = Math.max(fallbackHeaderAtMs, boundary.fallbackHeaderAtMs);
    fallbackHeaderBaselinePending =
      fallbackHeaderBaselinePending || boundary.fallbackHeaderBaselinePending === true;
    fallbackActionAtMs = Math.max(fallbackActionAtMs, boundary.fallbackActionAtMs);
    fallbackActionBaselinePending =
      fallbackActionBaselinePending || boundary.fallbackActionBaselinePending === true;
    fallbackCooldownAtMs = Math.max(fallbackCooldownAtMs, boundary.fallbackCooldownAtMs);
    fallbackCooldownBaselinePending =
      fallbackCooldownBaselinePending || boundary.fallbackCooldownBaselinePending === true;
    authenticationAtMs = Math.max(authenticationAtMs, boundary.authenticationAtMs ?? 0);
    rawStatusAtMs = Math.max(rawStatusAtMs, boundary.rawStatusAtMs);
    (boundary.rawStatusMessages ?? []).forEach((message) => rawStatusMessages.add(message));
    (boundary.rawStatusCodes ?? []).forEach((statusCode) => rawStatusCodes.add(statusCode));
  });
  if (
    localAtMs === 0 &&
    inspectionAtMs === 0 &&
    headerAtMs === 0 &&
    actionAtMs === 0 &&
    authenticationActionAtMs === 0 &&
    quotaActionAtMs === 0 &&
    cooldownAtMs === 0 &&
    fallbackInspectionAtMs === 0 &&
    fallbackHeaderAtMs === 0 &&
    fallbackActionAtMs === 0 &&
    fallbackCooldownAtMs === 0 &&
    authenticationAtMs === 0 &&
    rawStatusAtMs === 0 &&
    rawStatusMessages.size === 0 &&
    rawStatusCodes.size === 0
  ) {
    return EMPTY_ACCOUNT_CREDENTIAL_EVIDENCE_BOUNDARY;
  }
  return {
    localAtMs,
    inspectionAtMs,
    inspectionBaselinePending,
    headerAtMs,
    headerBaselinePending,
    actionAtMs,
    actionBaselinePending,
    authenticationActionAtMs,
    authenticationActionBaselinePending,
    quotaActionAtMs,
    quotaActionBaselinePending,
    cooldownAtMs,
    cooldownBaselinePending,
    fallbackInspectionAtMs,
    fallbackInspectionBaselinePending,
    fallbackHeaderAtMs,
    fallbackHeaderBaselinePending,
    fallbackActionAtMs,
    fallbackActionBaselinePending,
    fallbackCooldownAtMs,
    fallbackCooldownBaselinePending,
    authenticationAtMs,
    rawStatusAtMs,
    rawStatusMessages: Array.from(rawStatusMessages),
    rawStatusCodes: Array.from(rawStatusCodes),
  };
};

const upsertAccountCredentialEvidenceBoundaries = (
  current: ReadonlyMap<string, AccountCredentialEvidenceBoundary>,
  entries: Iterable<readonly [string, AccountCredentialEvidenceBoundary]>
): Map<string, AccountCredentialEvidenceBoundary> => {
  const next = new Map(current);
  for (const [key, boundary] of entries) {
    next.set(key, mergeAccountCredentialEvidenceBoundaries(next.get(key), boundary));
  }
  return next;
};

type AccountCredentialEvidenceBaselineSource = 'inspection' | 'header' | 'action' | 'cooldown';

interface AccountCredentialEvidenceBaselineIndex {
  exactBySelectionKey: ReadonlyMap<string, number>;
  anyByFileName: ReadonlyMap<string, number>;
  fallbackByFileName: ReadonlyMap<string, number>;
  defaultAtMs: number;
}

interface AccountCredentialEvidenceTimestampItem {
  fileName: string;
  identityKey: string;
  atMs: number;
}

const updateCredentialEvidenceTimestamp = (
  target: Map<string, number>,
  key: string,
  value: number
): void => {
  const normalizedKey = key.trim();
  if (!normalizedKey || !Number.isFinite(value) || value <= 0) return;
  target.set(normalizedKey, Math.max(target.get(normalizedKey) ?? 0, value));
};

const buildAccountCredentialEvidenceBaselineIndex = (
  files: readonly AuthFileItem[],
  items: readonly AccountCredentialEvidenceTimestampItem[],
  defaultAtMs = 0
): AccountCredentialEvidenceBaselineIndex => {
  const byIdentity = new Map<string, number>();
  const anyByFileName = new Map<string, number>();
  const fallbackByFileName = new Map<string, number>();
  items.forEach((item) => {
    const fileName = item.fileName.trim();
    if (!fileName) return;
    updateCredentialEvidenceTimestamp(byIdentity, item.identityKey, item.atMs);
    updateCredentialEvidenceTimestamp(anyByFileName, fileName, item.atMs);
    if (item.identityKey === getAuthFileCodexInspectionKeyForIdentity({ fileName })) {
      updateCredentialEvidenceTimestamp(fallbackByFileName, fileName, item.atMs);
    }
  });
  const exactBySelectionKey = new Map<string, number>();
  files.forEach((file) => {
    const atMs = byIdentity.get(getAuthFileCodexInspectionKeyForFile(file)) ?? 0;
    updateCredentialEvidenceTimestamp(exactBySelectionKey, getAuthFileSelectionKey(file), atMs);
  });
  return {
    exactBySelectionKey,
    anyByFileName,
    fallbackByFileName,
    defaultAtMs: Number.isFinite(defaultAtMs) && defaultAtMs > 0 ? Math.trunc(defaultAtMs) : 0,
  };
};

const resolveAccountCredentialEvidenceBaseline = (
  key: string,
  index: AccountCredentialEvidenceBaselineIndex
): { atMs: number; fallbackAtMs: number } => {
  if (key.startsWith(CREDENTIAL_EVIDENCE_SOURCE_FILE_BOUNDARY_PREFIX)) {
    const fileName = key.slice(CREDENTIAL_EVIDENCE_SOURCE_FILE_BOUNDARY_PREFIX.length);
    return {
      atMs: Math.max(index.defaultAtMs, index.anyByFileName.get(fileName) ?? 0),
      fallbackAtMs: Math.max(index.defaultAtMs, index.fallbackByFileName.get(fileName) ?? 0),
    };
  }
  if (key.startsWith(CREDENTIAL_EVIDENCE_PROVIDER_BOUNDARY_PREFIX)) {
    return { atMs: index.defaultAtMs, fallbackAtMs: index.defaultAtMs };
  }
  if (key.startsWith(CREDENTIAL_EVIDENCE_UNIQUE_FILE_NAME_BOUNDARY_PREFIX)) {
    const fileName = key.slice(CREDENTIAL_EVIDENCE_UNIQUE_FILE_NAME_BOUNDARY_PREFIX.length);
    return {
      atMs: index.defaultAtMs,
      fallbackAtMs: Math.max(index.defaultAtMs, index.fallbackByFileName.get(fileName) ?? 0),
    };
  }
  const fileName = getAuthFileNameFromSelectionKey(key).trim();
  return {
    atMs: Math.max(index.defaultAtMs, index.exactBySelectionKey.get(key) ?? 0),
    fallbackAtMs: Math.max(index.defaultAtMs, index.fallbackByFileName.get(fileName) ?? 0),
  };
};

const ACCOUNT_CREDENTIAL_EVIDENCE_BASELINE_FIELDS = {
  inspection: {
    atMs: 'inspectionAtMs',
    pending: 'inspectionBaselinePending',
    fallbackAtMs: 'fallbackInspectionAtMs',
    fallbackPending: 'fallbackInspectionBaselinePending',
  },
  header: {
    atMs: 'headerAtMs',
    pending: 'headerBaselinePending',
    fallbackAtMs: 'fallbackHeaderAtMs',
    fallbackPending: 'fallbackHeaderBaselinePending',
  },
  action: {
    atMs: 'actionAtMs',
    pending: 'actionBaselinePending',
    fallbackAtMs: 'fallbackActionAtMs',
    fallbackPending: 'fallbackActionBaselinePending',
  },
  cooldown: {
    atMs: 'cooldownAtMs',
    pending: 'cooldownBaselinePending',
    fallbackAtMs: 'fallbackCooldownAtMs',
    fallbackPending: 'fallbackCooldownBaselinePending',
  },
} as const;

const capturePendingAccountCredentialEvidenceBaseline = (
  current: Map<string, AccountCredentialEvidenceBoundary>,
  source: AccountCredentialEvidenceBaselineSource,
  index: AccountCredentialEvidenceBaselineIndex
): Map<string, AccountCredentialEvidenceBoundary> => {
  const fields = ACCOUNT_CREDENTIAL_EVIDENCE_BASELINE_FIELDS[source];
  let next: Map<string, AccountCredentialEvidenceBoundary> | null = null;
  current.forEach((boundary, key) => {
    if (boundary[fields.pending] !== true && boundary[fields.fallbackPending] !== true) return;

    const baseline = resolveAccountCredentialEvidenceBaseline(key, index);
    const updated = { ...boundary };
    if (boundary[fields.pending] === true) {
      updated[fields.atMs] = Math.max(updated[fields.atMs], baseline.atMs);
      updated[fields.pending] = false;
    }
    if (boundary[fields.fallbackPending] === true) {
      updated[fields.fallbackAtMs] = Math.max(updated[fields.fallbackAtMs], baseline.fallbackAtMs);
      updated[fields.fallbackPending] = false;
    }
    if (!next) next = new Map(current);
    next.set(key, updated);
  });
  return next ?? current;
};

const readAccountRawStatusMessage = (file: AuthFileItem): string => {
  const value = file.statusMessage ?? file['status_message'];
  return typeof value === 'string' ? value.trim() : '';
};

const buildProviderCredentialMutationBoundary = (
  createdAtMs: number
): AccountCredentialEvidenceBoundary => ({
  ...EMPTY_ACCOUNT_CREDENTIAL_EVIDENCE_BOUNDARY,
  localAtMs: createdAtMs,
  inspectionAtMs: createdAtMs,
  headerAtMs: createdAtMs,
  actionAtMs: createdAtMs,
  authenticationActionAtMs: createdAtMs,
  quotaActionAtMs: createdAtMs,
  cooldownAtMs: createdAtMs,
  fallbackInspectionAtMs: createdAtMs,
  fallbackHeaderAtMs: createdAtMs,
  fallbackActionAtMs: createdAtMs,
  fallbackCooldownAtMs: createdAtMs,
});

const buildCredentialMutationRawStatusBoundary = (
  file: AuthFileItem,
  createdAtMs: number
): AccountCredentialEvidenceBoundary | null => {
  const updatedAtMs = readAuthFileUpdatedAtMs(file);
  const rawStatusMessage = readAccountRawStatusMessage(file);
  const rawStatusCodes = getAuthFileCredentialStatusCodes(file);
  if (
    updatedAtMs === null ||
    updatedAtMs > createdAtMs ||
    (!rawStatusMessage && rawStatusCodes.length === 0)
  ) {
    return null;
  }
  return {
    ...EMPTY_ACCOUNT_CREDENTIAL_EVIDENCE_BOUNDARY,
    localAtMs: createdAtMs,
    rawStatusAtMs: createdAtMs,
    rawStatusMessages: rawStatusMessage ? [rawStatusMessage] : [],
    rawStatusCodes,
  };
};

const getCredentialEvidenceBoundaryForFile = (
  file: AuthFileItem,
  boundaries: ReadonlyMap<string, AccountCredentialEvidenceBoundary>,
  fileNameCounts: ReadonlyMap<string, number>
): AccountCredentialEvidenceBoundary =>
  mergeAccountCredentialEvidenceBoundaries(
    boundaries.get(getAuthFileSelectionKey(file)),
    boundaries.get(getCredentialEvidenceProviderBoundaryKey(normalizeAccountProvider(file))),
    boundaries.get(getCredentialEvidenceSourceFileBoundaryKey(file.name)),
    fileNameCounts.get(file.name) === 1
      ? boundaries.get(getCredentialEvidenceUniqueFileNameBoundaryKey(file.name))
      : undefined
  );

const readAccountsSearchFromHash = (hash: string): string => {
  const queryIndex = hash.indexOf('?');
  return queryIndex >= 0 ? hash.slice(queryIndex) : '';
};

const isConfigurationDetailTab = (tab: DetailTab): boolean => tab === 'config' || tab === 'models';

const getHealthStatusClass = (status: AccountListHealthStatusKey) => {
  switch (status) {
    case 'available':
      return styles.badgeGood;
    case 'five_hour_cooldown':
    case 'weekly_cooldown':
    case 'monthly_cooldown':
    case 'limited':
    case 'partial':
      return styles.badgeWarn;
    case 'five_hour_exhausted':
    case 'weekly_exhausted':
    case 'monthly_exhausted':
    case 'exception':
    case 'reauth':
      return styles.badgeBad;
    case 'disabled':
      return styles.badgeMuted;
    default:
      return styles.badgeNeutral;
  }
};

const getRemainingBarClass = (row: AccountRow) => {
  if (row.quota.status === 'exhausted' || row.quota.status === 'error') return styles.quotaBarBad;
  if (row.quota.status === 'low') return styles.quotaBarWarn;
  if (row.quota.status === 'ok') return styles.quotaBarGood;
  return styles.quotaBarNeutral;
};

const getWindowRemainingBarClass = (remainingPercent: number | null) => {
  if (remainingPercent === null) return styles.quotaBarNeutral;
  if (remainingPercent <= 0) return styles.quotaBarBad;
  if (remainingPercent < 20) return styles.quotaBarWarn;
  return styles.quotaBarGood;
};

const getFallbackWindowBarClass = (
  lifecycleBarOverride: AccountQuotaLifecycleBarOverride,
  remainingPercent: number | null
) => {
  if (lifecycleBarOverride === 'bad') return styles.quotaBarBad;
  if (lifecycleBarOverride === 'neutral') return styles.quotaBarNeutral;
  return getWindowRemainingBarClass(remainingPercent);
};

export function AccountsPage() {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const featureAvailability = usePanelFeatureAvailability();
  const initialWorkspaceUiState = useRef(readAccountsWorkspaceUiState());
  const initialWorkspaceUrlState = useRef(
    readAccountsWorkspaceUrlState(location.search, initialWorkspaceUiState.current)
  );
  const modelsLoadKeyRef = useRef('');
  const modelRulesLoadKeyRef = useRef('');
  const connectionFingerprint = useMemo(
    () => createCodexInspectionConnectionFingerprint(apiBase, managementKey) ?? '',
    [apiBase, managementKey]
  );
  const authFilesRequestScope = useMemo(
    () => ({ apiBase, managementKey }),
    [apiBase, managementKey]
  );
  const managerStorageAvailable =
    !featureAvailability.checking &&
    Boolean(featureAvailability.managerServiceBase) &&
    Boolean(managementKey);
  const managerConnectionFingerprint = useMemo(
    () =>
      createCodexInspectionConnectionFingerprint(
        featureAvailability.managerServiceBase ?? '',
        managementKey
      ),
    [featureAvailability.managerServiceBase, managementKey]
  );
  const requestHistoryAvailable =
    managerStorageAvailable && featureAvailability.requestMonitoringAvailable;
  const credentialMutationHandlerRef = useRef<(mutation: AuthFilesCredentialMutation) => void>(
    () => undefined
  );
  const handleCredentialMutation = useCallback((mutation: AuthFilesCredentialMutation) => {
    credentialMutationHandlerRef.current(mutation);
  }, []);

  const {
    files,
    selectedFiles,
    selectionCount,
    loading,
    error,
    uploading,
    authJsonPasteSaving,
    deleting,
    credentialRefreshing = {},
    batchFieldsUpdating,
    fileInputRef,
    loadFiles,
    handleUploadClick,
    handleFileChange,
    savePastedAuthJson,
    handleDelete,
    handleDownload,
    handleCredentialRefresh,
    toggleSelect,
    selectAllVisible,
    invertVisibleSelection,
    deselectAll,
    batchDownload,
    batchSetStatus,
    batchPatchFields,
    batchDelete,
  } = useAuthFilesData({
    connectionFingerprint,
    requestScope: authFilesRequestScope,
    onCredentialMutation: handleCredentialMutation,
  });

  const [oauthViewMode, setOauthViewMode] = useState<'diagram' | 'list'>('list');
  const oauthState = useAuthFilesOauth({
    viewMode: oauthViewMode,
    files,
    connectionKey: connectionFingerprint,
    requestScope: authFilesRequestScope,
  });
  const {
    modelsLoading,
    modelsRefreshing,
    modelsList,
    modelDefinitions,
    modelDefinitionsLoading,
    modelDefinitionsError,
    modelsFileName,
    modelsFileType,
    modelsSelectionKey,
    modelsError,
    showModels,
    refreshModels,
    invalidateModels,
  } = useAuthFilesModels({
    connectionKey: connectionFingerprint,
    requestScope: authFilesRequestScope,
  });
  const antigravityQuota = useQuotaStore((state) => state.antigravityQuota);
  const claudeQuota = useQuotaStore((state) => state.claudeQuota);
  const codexQuota = useQuotaStore((state) => state.codexQuota);
  const kimiQuota = useQuotaStore((state) => state.kimiQuota);
  const xaiQuota = useQuotaStore((state) => state.xaiQuota);
  const baseQuotaStores = useMemo(
    () => ({
      antigravityQuota,
      claudeQuota,
      codexQuota,
      kimiQuota,
      xaiQuota,
    }),
    [antigravityQuota, claudeQuota, codexQuota, kimiQuota, xaiQuota]
  );
  const setAntigravityQuota = useQuotaStore((state) => state.setAntigravityQuota);
  const setClaudeQuota = useQuotaStore((state) => state.setClaudeQuota);
  const setCodexQuota = useQuotaStore((state) => state.setCodexQuota);
  const setKimiQuota = useQuotaStore((state) => state.setKimiQuota);
  const setXaiQuota = useQuotaStore((state) => state.setXaiQuota);

  const [activeView, setActiveView] = useState<AccountsView>(
    () => initialWorkspaceUrlState.current.view
  );
  const [documentVisible, setDocumentVisible] = useState(
    () => typeof document === 'undefined' || document.visibilityState !== 'hidden'
  );
  const previousDocumentVisibleRef = useRef(documentVisible);
  const [healthMode, setHealthMode] = useState<CredentialHealthInspectionMode>(
    () => initialWorkspaceUrlState.current.healthMode
  );
  const headerSnapshotRequestGenerationRef = useRef(0);
  const {
    snapshot: inspectionSnapshot,
    results: inspectionResults,
    loading: inspectionLoading,
    refresh: loadInspectionSummary,
    applySnapshot: applyInspectionSnapshot,
    invalidatePendingRefresh: invalidateInspectionSummaryRequest,
  } = useCredentialInspectionSnapshot({
    connectionFingerprint,
    checking: featureAvailability.checking,
    serverAvailable: featureAvailability.serverCodexInspectionAvailable,
    managerServiceBase: featureAvailability.managerServiceBase ?? '',
    managementKey,
  });
  const inspectionSnapshotRef = useRef(inspectionSnapshot);
  inspectionSnapshotRef.current = inspectionSnapshot;
  const [quotaRefreshing, setQuotaRefreshing] = useState(false);
  const [historyRefreshing, setHistoryRefreshing] = useState(false);
  const [accountHistoryRefreshRevision, setAccountHistoryRefreshRevision] = useState(0);
  const [accountHistoryAutoRefreshRevision, setAccountHistoryAutoRefreshRevision] = useState(0);
  const [accountQuotaRefreshRevision, setAccountQuotaRefreshRevision] = useState(0);
  const [suppressedInspectionResultKeys, setSuppressedInspectionResultKeys] = useState<Set<string>>(
    () => readCompletedAccountReauthResultKeys(connectionFingerprint)
  );
  const [statusUpdating, setStatusUpdating] = useState(false);
  const [selectedRowKey, setSelectedRowKey] = useState<string | null>(
    () => initialWorkspaceUrlState.current.account
  );
  const [isSelectionMode, setIsSelectionMode] = useState(false);
  const [detailTab, setDetailTab] = useState<DetailTab>(
    () => initialWorkspaceUrlState.current.detailTab
  );
  const [providerFilter, setProviderFilter] = useState(
    () => initialWorkspaceUrlState.current.providerFilter
  );
  const [statusFilter, setStatusFilter] = useState<AccountStatusFilter>(
    () => initialWorkspaceUrlState.current.statusFilter
  );
  const [planFilter, setPlanFilter] = useState(() => initialWorkspaceUrlState.current.planFilter);
  const [quotaBandFilter, setQuotaBandFilter] = useState<AccountQuotaBand>(
    () => initialWorkspaceUrlState.current.quotaBandFilter
  );
  const [operationalFilter, setOperationalFilter] = useState<AccountOperationalFilter>(
    () => initialWorkspaceUrlState.current.operationalFilter
  );
  const effectiveOperationalFilter =
    !managerStorageAvailable &&
    (operationalFilter === 'cooldown' || operationalFilter === 'automation')
      ? 'all'
      : operationalFilter;
  const [search, setSearch] = useState(() => initialWorkspaceUrlState.current.search);
  const [accountSort, setAccountSort] = useState<AccountRowSort>(
    () => initialWorkspaceUrlState.current.accountSort
  );
  const [isMobileFiltersOpen, setIsMobileFiltersOpen] = useState(false);
  const [isAccountSortDropdownOpen, setIsAccountSortDropdownOpen] = useState(false);
  const [highlightedAccountSortIndex, setHighlightedAccountSortIndex] = useState(-1);
  const [batchPriorityOpen, setBatchPriorityOpen] = useState(false);
  const [batchPriorityValue, setBatchPriorityValue] = useState('');
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(() => initialWorkspaceUrlState.current.pageSize);
  const [usageRows, setUsageRows] = useState<UsageValueRow[]>([]);
  const [accountHistoryByRowKey, setAccountHistoryByRowKey] = useState<
    Map<string, MonitoringAccountHistoryItem>
  >(() => new Map());
  const [accountHistoryLoading, setAccountHistoryLoading] = useState(false);
  const [accountHistoryErrorsByRowKey, setAccountHistoryErrorsByRowKey] = useState<
    Map<string, string>
  >(() => new Map());
  const [accountWindowUsageByKey, setAccountWindowUsageByKey] = useState<
    Map<string, MonitoringAccountWindowUsageItem>
  >(() => new Map());
  const [accountWindowUsageQueryContext, setAccountWindowUsageQueryContext] = useState<{
    rowKey: string;
    asOfMs: number;
  } | null>(null);
  const [accountWindowUsageError, setAccountWindowUsageError] = useState('');
  const [quotaSnapshotWindowsByRowKey, setQuotaSnapshotWindowsByRowKey] = useState<
    Map<string, AccountQuotaSnapshotWindow[]>
  >(() => new Map());
  const [resettingQuotaKeys, setResettingQuotaKeys] = useState<ReadonlySet<string>>(
    () => new Set()
  );
  const [accountActionCandidates, setAccountActionCandidates] = useState<AccountActionCandidate[]>(
    []
  );
  const [accountActionCandidatesLoading, setAccountActionCandidatesLoading] = useState(false);
  const [accountActionCandidatesError, setAccountActionCandidatesError] = useState('');
  const [oauthPreviewModel, setOauthPreviewModel] = useState('');
  const [oauthPreviewProvider, setOauthPreviewProvider] = useState('');
  const [oauthDirectExpanded, setOauthDirectExpanded] = useState(false);
  const [oauthExcludedEditorProvider, setOauthExcludedEditorProvider] = useState<string | null>(
    () =>
      initialWorkspaceUrlState.current.editor === 'excluded'
        ? initialWorkspaceUrlState.current.editorProvider
        : null
  );
  const [oauthModelAliasEditorProvider, setOauthModelAliasEditorProvider] = useState<string | null>(
    () =>
      initialWorkspaceUrlState.current.editor === 'alias'
        ? initialWorkspaceUrlState.current.editorProvider
        : null
  );
  const oauthEditorConnectionFingerprintRef = useRef(connectionFingerprint);
  const [authJsonPasteOpen, setAuthJsonPasteOpen] = useState(false);
  const [codexReauthTarget, setCodexReauthTarget] = useState<CodexReauthTarget | null>(null);
  const codexReauthBaselineRef = useRef<AccountDirectReauthBaseline | null>(null);
  const inspectionCodexReauthBaselineRef = useRef<{
    scopeKey: string;
    target: CodexReauthTarget;
    baseline: AccountDirectReauthBaseline;
  } | null>(null);
  const [detailEventsRowKey, setDetailEventsRowKey] = useState<string | null>(null);
  const [detailEvents, setDetailEvents] = useState<MonitoringAnalyticsEventRow[]>([]);
  const [detailEventsSummary, setDetailEventsSummary] = useState<MonitoringAnalyticsSummary | null>(
    null
  );
  const [detailEventsRecentFailure, setDetailEventsRecentFailure] =
    useState<MonitoringAnalyticsRecentFailure | null>(null);
  const [detailEventsTotalCount, setDetailEventsTotalCount] = useState(0);
  const [detailEventsHasMore, setDetailEventsHasMore] = useState(false);
  const [detailEventsNextBeforeMs, setDetailEventsNextBeforeMs] = useState<number | null>(null);
  const [detailEventsNextBeforeId, setDetailEventsNextBeforeId] = useState<number | null>(null);
  const [detailEventsLoading, setDetailEventsLoading] = useState(false);
  const [detailEventsRefreshing, setDetailEventsRefreshing] = useState(false);
  const [detailEventsAppending, setDetailEventsAppending] = useState(false);
  const [detailEventsError, setDetailEventsError] = useState('');
  const [quotaCooldowns, setQuotaCooldowns] = useState<Map<string, QuotaCooldownInfo>>(
    () => new Map()
  );
  const credentialEvidenceScopeKey = useMemo(
    () =>
      createCredentialInspectionSnapshotScopeKey(
        connectionFingerprint,
        featureAvailability.managerServiceBase ?? '',
        managementKey
      ),
    [connectionFingerprint, featureAvailability.managerServiceBase, managementKey]
  );
  const activeCredentialEvidenceScopeKeyRef = useRef(credentialEvidenceScopeKey);
  activeCredentialEvidenceScopeKeyRef.current = credentialEvidenceScopeKey;
  const [credentialEvidenceBoundarySessionState, setCredentialEvidenceBoundarySessionState] =
    useState<AccountCredentialEvidenceBoundarySessionState>(() => ({
      scopeKey: credentialEvidenceScopeKey,
      ...loadAccountCredentialEvidenceBoundaryState(credentialEvidenceScopeKey),
    }));
  const credentialEvidenceBoundaries = useMemo(
    () =>
      credentialEvidenceBoundarySessionState.scopeKey === credentialEvidenceScopeKey
        ? credentialEvidenceBoundarySessionState.evidence
        : new Map<string, AccountCredentialEvidenceBoundary>(),
    [credentialEvidenceBoundarySessionState, credentialEvidenceScopeKey]
  );
  const credentialStatusBoundaries = useMemo(
    () =>
      credentialEvidenceBoundarySessionState.scopeKey === credentialEvidenceScopeKey
        ? credentialEvidenceBoundarySessionState.status
        : new Map<string, AccountCredentialEvidenceBoundary>(),
    [credentialEvidenceBoundarySessionState, credentialEvidenceScopeKey]
  );
  const setCredentialEvidenceBoundaries = useCallback(
    (update: SetStateAction<Map<string, AccountCredentialEvidenceBoundary>>) => {
      setCredentialEvidenceBoundarySessionState((current) => {
        const active =
          current.scopeKey === credentialEvidenceScopeKey
            ? current
            : {
                scopeKey: credentialEvidenceScopeKey,
                ...loadAccountCredentialEvidenceBoundaryState(credentialEvidenceScopeKey),
              };
        const evidence = typeof update === 'function' ? update(active.evidence) : update;
        return evidence === active.evidence && active === current
          ? current
          : { ...active, evidence };
      });
    },
    [credentialEvidenceScopeKey]
  );
  const setCredentialStatusBoundaries = useCallback(
    (update: SetStateAction<Map<string, AccountCredentialEvidenceBoundary>>) => {
      setCredentialEvidenceBoundarySessionState((current) => {
        const active =
          current.scopeKey === credentialEvidenceScopeKey
            ? current
            : {
                scopeKey: credentialEvidenceScopeKey,
                ...loadAccountCredentialEvidenceBoundaryState(credentialEvidenceScopeKey),
              };
        const status = typeof update === 'function' ? update(active.status) : update;
        return status === active.status && active === current ? current : { ...active, status };
      });
    },
    [credentialEvidenceScopeKey]
  );
  const headerSnapshotLoadKey = useMemo(() => {
    return JSON.stringify({
      connectionFingerprint,
      managerConnectionFingerprint,
      requestMonitoringAvailable: featureAvailability.requestMonitoringAvailable,
    });
  }, [
    connectionFingerprint,
    featureAvailability.requestMonitoringAvailable,
    managerConnectionFingerprint,
  ]);
  const headerSnapshots = useUsageHeaderSnapshotStore((state) => state.items);
  const headerSnapshotGeneratedAtMs = useUsageHeaderSnapshotStore((state) => state.generatedAtMs);
  const headerSnapshotsLoadedAtMs = useUsageHeaderSnapshotStore((state) => state.loadedAtMs);
  const [headerSnapshotLoadedKey, setHeaderSnapshotLoadedKey] = useState('');
  const headerSnapshotLoadKeyRef = useRef(headerSnapshotLoadKey);
  headerSnapshotLoadKeyRef.current = headerSnapshotLoadKey;
  const [accountDisplayMode, setAccountDisplayMode] = useState<QuotaAccountDisplayMode>(
    () => initialWorkspaceUrlState.current.accountDisplayMode
  );
  const [copiedIdentityKey, setCopiedIdentityKey] = useState<string | null>(null);
  const detailEventsRequestIdRef = useRef(0);
  const detailEventsAutoLoadKeyRef = useRef<string | null>(null);
  const quotaCooldownRequestIdRef = useRef(0);
  const accountHistoryAutoAbortRef = useRef<AbortController | null>(null);
  const accountHistoryTargetAbortRef = useRef<AbortController | null>(null);
  const accountHistoryRequestVersionsRef = useRef<Map<string, symbol>>(new Map());
  const accountHistoryPendingRequestsRef = useRef<Set<symbol>>(new Set());
  const accountHistoryAutoLoadKeyRef = useRef<string | null>(null);
  const accountHistoryAutoRequestContextKeyRef = useRef<string | null>(null);
  const accountWindowUsageReqIdRef = useRef(0);
  const accountWindowUsageAbortRef = useRef<AbortController | null>(null);
  const accountWindowUsageAutoLoadKeyRef = useRef<string | null>(null);
  const quotaRefreshBatchRef = useRef<{
    connectionFingerprint: string;
    generation: number;
    promise: Promise<void>;
  } | null>(null);
  const quotaRefreshGenerationRef = useRef(0);
  const accountHistoryRefreshRequestIdRef = useRef(0);
  const accountHistoryRefreshPromiseRef = useRef<{
    key: string;
    promise: Promise<void>;
  } | null>(null);
  const usageValuesRequestIdRef = useRef(0);
  const usageValuesAutoLoadKeyRef = useRef<string | null>(null);
  const accountActionCandidatesReqIdRef = useRef(0);
  const accountActionCandidatesRef = useRef<AccountActionCandidate[]>([]);
  const quotaCooldownsRef = useRef<Map<string, QuotaCooldownInfo>>(new Map());
  const accountActionCandidatesLoadedRef = useRef(false);
  const quotaCooldownsLoadedRef = useRef(false);
  const consumedCredentialMutationMarkerIdsRef = useRef<Set<string>>(new Set());
  const credentialMutationMarkerAttemptsRef = useRef<Map<string, string>>(new Map());
  const credentialMutationMarkerExhaustedRef = useRef<Map<string, string>>(new Map());
  const credentialMutationMarkerSynchronizationsRef = useRef<Map<string, Promise<boolean>>>(
    new Map()
  );
  const isMountedRef = useRef(true);
  const directReauthSynchronizationsRef = useRef<Map<string, Promise<boolean>>>(new Map());
  const synchronizePendingAccountDirectReauthsRef = useRef<
    (options?: { reload?: boolean }) => Promise<boolean>
  >(async () => false);
  const synchronizedInspectionMutationKeysRef = useRef<Set<string>>(new Set());
  const synchronizingInspectionMutationsRef = useRef<Map<string, Promise<boolean>>>(new Map());
  const pendingInspectionMutationSnapshotsRef = useRef<Map<string, CredentialInspectionSnapshot>>(
    new Map()
  );
  const capturePendingInspectionEvidenceBaselineRef = useRef<
    (snapshot: CredentialInspectionSnapshot) => void
  >(() => undefined);
  const capturePendingHeaderEvidenceBaselineRef = useRef<
    (response: UsageHeaderSnapshotsResponse) => void
  >(() => undefined);
  const capturePendingActionEvidenceBaselineRef = useRef<
    (items: readonly AccountActionCandidate[]) => void
  >(() => undefined);
  const capturePendingCooldownEvidenceBaselineRef = useRef<
    (items: readonly QuotaCooldownInfo[]) => void
  >(() => undefined);
  const invalidateProviderCredentialEvidenceRef = useRef<
    (
      provider: string,
      createdAtMs: number,
      options?: {
        credentialFiles?: readonly AuthFileItem[];
        inventoryFiles?: readonly AuthFileItem[];
        supersedeRequests?: boolean;
        scope?: 'provider' | 'credential';
      }
    ) => AuthFileItem[]
  >(() => []);
  const lastWorkspaceNavigationRef = useRef<string | null>(null);
  const syncingWorkspaceLocationRef = useRef(false);
  const hasProcessedInitialWorkspaceLocationRef = useRef(false);
  const currentHashHistoryStateRef = useRef<unknown>(
    typeof window === 'undefined' ? null : window.history?.state
  );
  const pendingExternalHashNavigationRef = useRef('');
  const quotaRequestVersionsRef = useRef<Map<string, number>>(new Map());
  const resettingQuotaKeysRef = useRef<Set<string>>(new Set());
  const identityCopyTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const accountSortDropdownRef = useRef<HTMLDivElement | null>(null);
  const accountSortTriggerRef = useRef<HTMLButtonElement | null>(null);
  const accountSortOptionRefs = useRef<Map<AccountSortFieldValue, HTMLButtonElement | null>>(
    new Map()
  );
  const detailDrawerBodyRef = useRef<HTMLDivElement | null>(null);
  const selectedRowKeyRef = useRef(selectedRowKey);
  const headerSnapshotContextRef = useRef({
    managerServiceBase: featureAvailability.managerServiceBase,
    managementKey,
    connectionFingerprint,
    checking: featureAvailability.checking,
    requestMonitoringAvailable: featureAvailability.requestMonitoringAvailable,
  });

  useEffect(() => {
    isMountedRef.current = true;
    return () => {
      isMountedRef.current = false;
    };
  }, []);

  useLayoutEffect(() => {
    const previousConnectionFingerprint = oauthEditorConnectionFingerprintRef.current;
    oauthEditorConnectionFingerprintRef.current = connectionFingerprint;
    if (previousConnectionFingerprint === connectionFingerprint) return;
    setOauthExcludedEditorProvider(null);
    setOauthModelAliasEditorProvider(null);
    setAuthJsonPasteOpen(false);
    setCodexReauthTarget(null);
    codexReauthBaselineRef.current = null;
    inspectionCodexReauthBaselineRef.current = null;
    credentialMutationMarkerAttemptsRef.current.clear();
    credentialMutationMarkerExhaustedRef.current.clear();
    quotaRefreshGenerationRef.current += 1;
    quotaRefreshBatchRef.current = null;
    quotaRequestVersionsRef.current.forEach((version, key) => {
      quotaRequestVersionsRef.current.set(key, version + 1);
    });
    setQuotaRefreshing(false);
  }, [connectionFingerprint]);

  useLayoutEffect(() => {
    setCredentialEvidenceBoundarySessionState((current) =>
      current.scopeKey === credentialEvidenceScopeKey
        ? current
        : {
            scopeKey: credentialEvidenceScopeKey,
            ...loadAccountCredentialEvidenceBoundaryState(credentialEvidenceScopeKey),
          }
    );
  }, [credentialEvidenceScopeKey]);

  useLayoutEffect(() => {
    if (
      !credentialEvidenceScopeKey ||
      credentialEvidenceBoundarySessionState.scopeKey !== credentialEvidenceScopeKey
    ) {
      return;
    }
    saveAccountCredentialEvidenceBoundaryState(credentialEvidenceScopeKey, {
      evidence: credentialEvidenceBoundarySessionState.evidence,
      status: credentialEvidenceBoundarySessionState.status,
    });
  }, [credentialEvidenceBoundarySessionState, credentialEvidenceScopeKey]);

  useLayoutEffect(() => {
    if (
      featureAvailability.checking ||
      managerStorageAvailable ||
      (operationalFilter !== 'cooldown' && operationalFilter !== 'automation')
    ) {
      return;
    }
    setOperationalFilter('all');
  }, [featureAvailability.checking, managerStorageAvailable, operationalFilter]);

  const loadQuotaCooldowns = useCallback(
    async (options: { throwOnError?: boolean } = {}) => {
      const requestId = quotaCooldownRequestIdRef.current + 1;
      quotaCooldownRequestIdRef.current = requestId;
      if (!featureAvailability.managerServiceBase) {
        quotaCooldownsRef.current = new Map();
        setQuotaCooldowns((current) => (current.size === 0 ? current : new Map()));
        return;
      }

      try {
        const items = await usageServiceApi.getActiveQuotaCooldowns(
          featureAvailability.managerServiceBase,
          managementKey
        );
        if (quotaCooldownRequestIdRef.current !== requestId) {
          if (options.throwOnError) throw new Error(t('notification.load_failed'));
          return;
        }
        quotaCooldownsLoadedRef.current = true;
        capturePendingCooldownEvidenceBaselineRef.current(items);
        const next = new Map<string, QuotaCooldownInfo>();
        for (const item of items) {
          if (!item.authFileName) continue;
          const key = getAuthFileCodexInspectionKeyForIdentity({
            fileName: item.authFileName,
            provider: item.provider,
            authIndex: item.authIndex ?? null,
            accountSnapshot: item.accountSnapshot,
          });
          const existing = next.get(key);
          if (!existing || (item.recoverAtMs ?? 0) > (existing.recoverAtMs ?? 0)) {
            next.set(key, item);
          }
        }
        quotaCooldownsRef.current = next;
        setQuotaCooldowns(next);
      } catch (error) {
        // Cooldown badges are a derived hint; keep the last known state on transient failures.
        if (options.throwOnError) throw error;
      }
    },
    [featureAvailability.managerServiceBase, managementKey, t]
  );

  const loadSharedHeaderSnapshots = useHeaderSnapshotsLoader({
    serviceBase:
      !featureAvailability.checking && featureAvailability.requestMonitoringAvailable
        ? featureAvailability.managerServiceBase
        : '',
    managementKey,
    requestGenerationRef: headerSnapshotRequestGenerationRef,
    onResponse: (response) => capturePendingHeaderEvidenceBaselineRef.current(response),
  });
  const loadHeaderSnapshots = useCallback(async () => {
    if (featureAvailability.checking) return;
    try {
      await loadSharedHeaderSnapshots();
    } finally {
      if (headerSnapshotLoadKeyRef.current === headerSnapshotLoadKey) {
        setHeaderSnapshotLoadedKey(headerSnapshotLoadKey);
      }
    }
  }, [featureAvailability.checking, headerSnapshotLoadKey, loadSharedHeaderSnapshots]);

  const loadAccountActionCandidates = useCallback(
    async (options: { throwOnError?: boolean } = {}) => {
      const requestId = accountActionCandidatesReqIdRef.current + 1;
      accountActionCandidatesReqIdRef.current = requestId;

      if (featureAvailability.checking || !featureAvailability.managerServiceBase) {
        accountActionCandidatesRef.current = [];
        setAccountActionCandidates([]);
        setAccountActionCandidatesLoading(false);
        setAccountActionCandidatesError('');
        return;
      }

      setAccountActionCandidatesLoading(true);
      setAccountActionCandidatesError('');
      try {
        const response = await usageServiceApi.listAccountActionCandidates(
          featureAvailability.managerServiceBase,
          managementKey,
          'pending',
          500
        );
        if (accountActionCandidatesReqIdRef.current !== requestId) {
          if (options.throwOnError) throw new Error(t('notification.load_failed'));
          return;
        }
        const items = response.items ?? [];
        accountActionCandidatesLoadedRef.current = true;
        capturePendingActionEvidenceBaselineRef.current(items);
        accountActionCandidatesRef.current = items;
        setAccountActionCandidates(items);
      } catch (err: unknown) {
        if (accountActionCandidatesReqIdRef.current !== requestId) {
          if (options.throwOnError) throw err;
          return;
        }
        setAccountActionCandidatesError(
          err instanceof Error ? err.message : t('notification.load_failed')
        );
        if (options.throwOnError) throw err;
      } finally {
        if (accountActionCandidatesReqIdRef.current === requestId) {
          setAccountActionCandidatesLoading(false);
        }
      }
    },
    [featureAvailability.checking, featureAvailability.managerServiceBase, managementKey, t]
  );

  const reloadInspectionCredentialArtifacts = useCallback(
    async (
      options: {
        requireSuccessfulReload?: boolean;
        loadCredentialsLast?: boolean;
      } = {}
    ): Promise<AuthFileItem[] | undefined> => {
      const credentialReloadOptions = options.requireSuccessfulReload
        ? { throwOnError: true }
        : undefined;
      const operationalReloads = [
        loadQuotaCooldowns({ throwOnError: options.requireSuccessfulReload }),
        loadAccountActionCandidates({ throwOnError: options.requireSuccessfulReload }),
      ];
      if (options.loadCredentialsLast) {
        await Promise.all(operationalReloads);
        return loadFiles(credentialReloadOptions);
      }
      const [reloadedFiles] = await Promise.all([
        loadFiles(credentialReloadOptions),
        ...operationalReloads,
      ]);
      return reloadedFiles;
    },
    [loadAccountActionCandidates, loadFiles, loadQuotaCooldowns]
  );

  const synchronizeExternalCredentialMutationMarkers = useCallback(
    async (options: { force?: boolean } = {}) => {
      const force = options.force === true;
      const synchronizationScopeKey = credentialEvidenceScopeKey;
      const markers = listAccountCredentialMutationMarkers(connectionFingerprint).filter(
        (marker) => {
          if (consumedCredentialMutationMarkerIdsRef.current.has(marker.id)) return false;
          if (force) return true;
          const currentEvidence = JSON.stringify(
            createAccountCredentialMutationBaseline(files, marker.provider)
          );
          const exhaustedEvidence = credentialMutationMarkerExhaustedRef.current.get(marker.id);
          return exhaustedEvidence !== `${synchronizationScopeKey}\u001e${currentEvidence}`;
        }
      );
      if (markers.length === 0) return false;
      const pendingSynchronization =
        credentialMutationMarkerSynchronizationsRef.current.get(synchronizationScopeKey);
      if (pendingSynchronization) return pendingSynchronization;

      const synchronization = (async () => {
        try {
          const markersByProvider = new Map<string, AccountCredentialMutationMarker[]>();
          markers.forEach((marker) => {
            const providerMarkers = markersByProvider.get(marker.provider) ?? [];
            providerMarkers.push(marker);
            markersByProvider.set(marker.provider, providerMarkers);
          });

          const retry = await runCredentialVisibilityRetry<AuthFileItem[]>({
            load: async () => {
              const reloadedFiles = await reloadInspectionCredentialArtifacts({
                requireSuccessfulReload: true,
                loadCredentialsLast: true,
              });
              if (!reloadedFiles) throw new Error(t('notification.refresh_failed'));
              return reloadedFiles;
            },
            isUnconfirmed: (reloadedFiles) =>
              markers.some(
                (marker) => !hasAccountCredentialMutationEvidence(marker, reloadedFiles)
              ),
            isActive: () =>
              isMountedRef.current &&
              activeCredentialEvidenceScopeKeyRef.current === synchronizationScopeKey,
          });
          if (
            retry.cancelled ||
            activeCredentialEvidenceScopeKeyRef.current !== synchronizationScopeKey ||
            !retry.value
          ) {
            return false;
          }
          const reloadedFiles = retry.value;
          const consumedIds: string[] = [];
          markersByProvider.forEach((providerMarkers, provider) => {
            const observedEvidence = JSON.stringify(
              createAccountCredentialMutationBaseline(reloadedFiles, provider)
            );
            const evidencedMarkers = providerMarkers.filter((marker) =>
              hasAccountCredentialMutationEvidence(marker, reloadedFiles)
            );
            providerMarkers
              .filter((marker) => !evidencedMarkers.includes(marker))
              .forEach((marker) => {
                credentialMutationMarkerAttemptsRef.current.set(marker.id, observedEvidence);
                if (retry.exhausted) {
                  credentialMutationMarkerExhaustedRef.current.set(
                    marker.id,
                    `${synchronizationScopeKey}\u001e${observedEvidence}`
                  );
                }
              });
            const sortedEvidencedMarkers = [...evidencedMarkers].sort(
              (left, right) =>
                left.createdAtMs - right.createdAtMs || left.id.localeCompare(right.id)
            );
            if (sortedEvidencedMarkers.length === 0) return;
            let providerConsumed = false;
            sortedEvidencedMarkers.forEach((marker) => {
              const markerFiles = marker.requireObservedMutation
                ? resolveAccountCredentialMutationFiles(marker, reloadedFiles)
                : reloadedFiles.filter((file) => normalizeAccountProvider(file) === provider);
              const affectedFilesBySelectionKey = new Map<string, AuthFileItem>();
              markerFiles.forEach((file) => {
                affectedFilesBySelectionKey.set(getAuthFileSelectionKey(file), file);
              });
              const targetFiles = invalidateProviderCredentialEvidenceRef.current(
                provider,
                marker.createdAtMs,
                {
                  credentialFiles: Array.from(affectedFilesBySelectionKey.values()),
                  inventoryFiles: reloadedFiles,
                  supersedeRequests: false,
                  scope: marker.requireObservedMutation ? 'credential' : 'provider',
                }
              );
              if (targetFiles.length === 0) return;
              consumedIds.push(marker.id);
              credentialMutationMarkerAttemptsRef.current.delete(marker.id);
              credentialMutationMarkerExhaustedRef.current.delete(marker.id);
              providerConsumed = true;
            });
            if (!providerConsumed) return;
            publishAccountCredentialMutationRevision({
              connectionFingerprint,
              provider,
              kind: 'oauth',
            });
          });
          if (consumedIds.length === 0) return false;
          consumedIds.forEach((id) => consumedCredentialMutationMarkerIdsRef.current.add(id));
          acknowledgeAccountCredentialMutationMarkers(consumedIds);
          return true;
        } catch {
          // Keep the marker for a future explicit refresh when auth-files is unavailable.
          return false;
        } finally {
          credentialMutationMarkerSynchronizationsRef.current.delete(synchronizationScopeKey);
        }
      })();
      credentialMutationMarkerSynchronizationsRef.current.set(
        synchronizationScopeKey,
        synchronization
      );
      return synchronization;
    },
    [
      connectionFingerprint,
      credentialEvidenceScopeKey,
      files,
      reloadInspectionCredentialArtifacts,
      t,
    ]
  );

  const getInspectionCredentialMutationScopedKey = useCallback(
    (
      snapshot: CredentialInspectionSnapshot | null | undefined,
      requireLatestCompletedRun: boolean
    ) => {
      if (!snapshot) return null;
      const mutationKey = getServerCredentialMutationSyncKey(snapshot, {
        requireLatestCompletedRun,
      });
      if (!mutationKey) return null;
      return [credentialEvidenceScopeKey, mutationKey].join('\u001e');
    },
    [credentialEvidenceScopeKey]
  );

  const synchronizeInspectionCredentialMutations = useCallback(
    async (
      snapshot: CredentialInspectionSnapshot | null | undefined,
      requireLatestCompletedRun: boolean
    ) => {
      const synchronizationScopeKey = credentialEvidenceScopeKey;
      if (activeCredentialEvidenceScopeKeyRef.current !== synchronizationScopeKey) return false;
      const scopedKey = getInspectionCredentialMutationScopedKey(
        snapshot,
        requireLatestCompletedRun
      );
      if (!scopedKey) {
        return false;
      }
      if (!requireLatestCompletedRun && snapshot) {
        pendingInspectionMutationSnapshotsRef.current.set(scopedKey, snapshot);
      }
      if (synchronizedInspectionMutationKeysRef.current.has(scopedKey)) {
        pendingInspectionMutationSnapshotsRef.current.delete(scopedKey);
        return false;
      }
      const pending = synchronizingInspectionMutationsRef.current.get(scopedKey);
      if (pending) return pending;

      const coveredPendingSnapshots = new Map(pendingInspectionMutationSnapshotsRef.current);
      const synchronization = (async () => {
        try {
          await reloadInspectionCredentialArtifacts({ requireSuccessfulReload: true });
          if (activeCredentialEvidenceScopeKeyRef.current !== synchronizationScopeKey) {
            return false;
          }
          synchronizedInspectionMutationKeysRef.current.add(scopedKey);
          coveredPendingSnapshots.forEach((pendingSnapshot, pendingKey) => {
            if (pendingInspectionMutationSnapshotsRef.current.get(pendingKey) !== pendingSnapshot) {
              return;
            }
            synchronizedInspectionMutationKeysRef.current.add(pendingKey);
            pendingInspectionMutationSnapshotsRef.current.delete(pendingKey);
          });
          return true;
        } catch {
          return false;
        } finally {
          synchronizingInspectionMutationsRef.current.delete(scopedKey);
        }
      })();
      synchronizingInspectionMutationsRef.current.set(scopedKey, synchronization);
      return synchronization;
    },
    [
      credentialEvidenceScopeKey,
      getInspectionCredentialMutationScopedKey,
      reloadInspectionCredentialArtifacts,
    ]
  );

  const retryPendingInspectionCredentialMutations = useCallback(async () => {
    const pendingSnapshot = Array.from(
      pendingInspectionMutationSnapshotsRef.current.entries()
    ).find(([key]) => key.startsWith(`${credentialEvidenceScopeKey}\u001e`))?.[1];
    if (!pendingSnapshot) return false;
    return synchronizeInspectionCredentialMutations(pendingSnapshot, false);
  }, [credentialEvidenceScopeKey, synchronizeInspectionCredentialMutations]);

  const loadInspectionSummaryAndSynchronize = useCallback(async () => {
    const refreshScopeKey = credentialEvidenceScopeKey;
    const snapshot = await loadInspectionSummary();
    if (activeCredentialEvidenceScopeKeyRef.current !== refreshScopeKey) return;
    if (snapshot) capturePendingInspectionEvidenceBaselineRef.current(snapshot);
    const synchronizedLatest = await synchronizeInspectionCredentialMutations(snapshot, true);
    if (!synchronizedLatest) {
      await retryPendingInspectionCredentialMutations();
    }
  }, [
    credentialEvidenceScopeKey,
    loadInspectionSummary,
    retryPendingInspectionCredentialMutations,
    synchronizeInspectionCredentialMutations,
  ]);

  useLayoutEffect(() => {
    const prev = headerSnapshotContextRef.current;
    const connectionScopeChanged =
      prev.managerServiceBase !== featureAvailability.managerServiceBase ||
      prev.managementKey !== managementKey ||
      prev.connectionFingerprint !== connectionFingerprint;
    const availabilityChanged =
      prev.checking !== featureAvailability.checking ||
      prev.requestMonitoringAvailable !== featureAvailability.requestMonitoringAvailable;
    if (!connectionScopeChanged && !availabilityChanged) {
      return;
    }
    headerSnapshotContextRef.current = {
      managerServiceBase: featureAvailability.managerServiceBase,
      managementKey,
      connectionFingerprint,
      checking: featureAvailability.checking,
      requestMonitoringAvailable: featureAvailability.requestMonitoringAvailable,
    };
    quotaCooldownRequestIdRef.current += 1;
    accountHistoryAutoAbortRef.current?.abort();
    accountHistoryAutoAbortRef.current = null;
    accountHistoryTargetAbortRef.current?.abort();
    accountHistoryTargetAbortRef.current = null;
    accountHistoryRefreshRequestIdRef.current += 1;
    accountHistoryRefreshPromiseRef.current = null;
    accountHistoryRequestVersionsRef.current.clear();
    accountHistoryPendingRequestsRef.current.clear();
    accountHistoryAutoLoadKeyRef.current = null;
    accountHistoryAutoRequestContextKeyRef.current = null;
    accountActionCandidatesReqIdRef.current += 1;
    accountActionCandidatesLoadedRef.current = false;
    quotaCooldownsLoadedRef.current = false;
    if (connectionScopeChanged) {
      synchronizedInspectionMutationKeysRef.current.clear();
      synchronizingInspectionMutationsRef.current.clear();
      pendingInspectionMutationSnapshotsRef.current.clear();
      directReauthSynchronizationsRef.current.clear();
    }
    accountWindowUsageReqIdRef.current += 1;
    accountWindowUsageAbortRef.current?.abort();
    accountWindowUsageAbortRef.current = null;
    accountActionCandidatesRef.current = [];
    accountWindowUsageAutoLoadKeyRef.current = null;
    usageValuesRequestIdRef.current += 1;
    usageValuesAutoLoadKeyRef.current = null;
    detailEventsRequestIdRef.current += 1;
    detailEventsAutoLoadKeyRef.current = null;
    quotaCooldownsRef.current = new Map();
    setQuotaCooldowns(new Map());
    setAccountActionCandidates([]);
    setHeaderSnapshotLoadedKey('');
    setAccountHistoryByRowKey((current) => (current.size === 0 ? current : new Map()));
    setAccountHistoryLoading(false);
    setAccountHistoryErrorsByRowKey((current) => (current.size === 0 ? current : new Map()));
    setHistoryRefreshing(false);
    setQuotaSnapshotWindowsByRowKey((current) => (current.size === 0 ? current : new Map()));
    setAccountWindowUsageByKey((current) => (current.size === 0 ? current : new Map()));
    setAccountWindowUsageQueryContext(null);
    setAccountWindowUsageError('');
    setUsageRows([]);
    setDetailEventsRowKey(null);
    setDetailEvents([]);
    setDetailEventsSummary(null);
    setDetailEventsRecentFailure(null);
    setDetailEventsTotalCount(0);
    setDetailEventsHasMore(false);
    setDetailEventsNextBeforeMs(null);
    setDetailEventsNextBeforeId(null);
    setDetailEventsLoading(false);
    setDetailEventsRefreshing(false);
    setDetailEventsAppending(false);
    setDetailEventsError('');
    setSuppressedInspectionResultKeys(readCompletedAccountReauthResultKeys(connectionFingerprint));
  }, [
    connectionFingerprint,
    featureAvailability.checking,
    featureAvailability.managerServiceBase,
    featureAvailability.requestMonitoringAvailable,
    managementKey,
  ]);

  useEffect(
    () => () => {
      accountHistoryAutoAbortRef.current?.abort();
      accountHistoryTargetAbortRef.current?.abort();
      accountWindowUsageAbortRef.current?.abort();
    },
    []
  );

  const loadOauthExcluded = oauthState.loadExcluded;
  const loadOauthModelAlias = oauthState.loadModelAlias;
  const refreshOauthWorkspace = useCallback(async () => {
    await Promise.all([loadOauthExcluded(), loadOauthModelAlias()]);
  }, [loadOauthExcluded, loadOauthModelAlias]);
  const refreshAccountsWorkspace = useCallback(async () => {
    const refreshScopeKey = credentialEvidenceScopeKey;
    const synchronizedDirectReauth = await synchronizePendingAccountDirectReauthsRef.current();
    if (activeCredentialEvidenceScopeKeyRef.current !== refreshScopeKey) return;
    const synchronizedExternalArtifacts = await synchronizeExternalCredentialMutationMarkers({
      force: true,
    });
    if (activeCredentialEvidenceScopeKeyRef.current !== refreshScopeKey) return;
    const snapshot = await loadInspectionSummary();
    if (activeCredentialEvidenceScopeKeyRef.current !== refreshScopeKey) return;
    if (snapshot) capturePendingInspectionEvidenceBaselineRef.current(snapshot);
    const synchronizedInspectionArtifacts = await synchronizeInspectionCredentialMutations(
      snapshot,
      true
    );
    const synchronizedPendingArtifacts = synchronizedInspectionArtifacts
      ? false
      : await retryPendingInspectionCredentialMutations();
    if (activeCredentialEvidenceScopeKeyRef.current !== refreshScopeKey) return;
    await Promise.all([
      synchronizedInspectionArtifacts || synchronizedPendingArtifacts
        ? Promise.resolve()
        : synchronizedDirectReauth || synchronizedExternalArtifacts
          ? Promise.resolve()
          : loadFiles(),
      loadHeaderSnapshots(),
    ]);
    setAccountHistoryAutoRefreshRevision((current) => current + 1);
  }, [
    credentialEvidenceScopeKey,
    loadFiles,
    loadHeaderSnapshots,
    loadInspectionSummary,
    retryPendingInspectionCredentialMutations,
    synchronizeExternalCredentialMutationMarkers,
    synchronizeInspectionCredentialMutations,
  ]);
  const refreshActiveWorkspace = useAccountsWorkspaceRefresh(activeView, {
    refreshAccounts: refreshAccountsWorkspace,
    refreshHealth: loadInspectionSummaryAndSynchronize,
    refreshOauth: refreshOauthWorkspace,
  });

  const canLoadHeaderSnapshots =
    activeView === 'accounts' &&
    !featureAvailability.checking &&
    featureAvailability.requestMonitoringAvailable &&
    Boolean(featureAvailability.managerServiceBase);
  const canLoadInspectionSummary =
    activeView === 'accounts' &&
    managerStorageAvailable &&
    featureAvailability.serverCodexInspectionAvailable;
  const refreshPassiveAccountsEvidence = useCallback(async () => {
    await Promise.all([
      canLoadHeaderSnapshots ? loadHeaderSnapshots() : Promise.resolve(),
      canLoadInspectionSummary ? loadInspectionSummaryAndSynchronize() : Promise.resolve(),
    ]);
    setAccountHistoryAutoRefreshRevision((current) => current + 1);
  }, [
    canLoadHeaderSnapshots,
    canLoadInspectionSummary,
    loadHeaderSnapshots,
    loadInspectionSummaryAndSynchronize,
  ]);

  useHeaderRefresh(refreshActiveWorkspace);

  useEffect(() => {
    void loadFiles();
  }, [connectionFingerprint, loadFiles]);

  useEffect(() => {
    if (!connectionFingerprint || loading || error) return;
    void synchronizePendingAccountDirectReauthsRef.current({ reload: false });
  }, [connectionFingerprint, error, files, loading]);

  useEffect(() => {
    if (!connectionFingerprint || loading || error) return;
    void synchronizeExternalCredentialMutationMarkers();
  }, [connectionFingerprint, error, files, loading, synchronizeExternalCredentialMutationMarkers]);

  useEffect(() => {
    if (loading || error) return;
    setCredentialEvidenceBoundaries((current) =>
      releaseObservedRawStatusBoundaries(current, files)
    );
    setCredentialStatusBoundaries((current) => releaseObservedRawStatusBoundaries(current, files));
  }, [error, files, loading, setCredentialEvidenceBoundaries, setCredentialStatusBoundaries]);

  useEffect(() => {
    if (activeView === 'accounts') return;
    void refreshActiveWorkspace();
  }, [activeView, connectionFingerprint, refreshActiveWorkspace]);

  useEffect(() => {
    if (typeof document === 'undefined') return;
    const updateVisibility = () => setDocumentVisible(document.visibilityState !== 'hidden');
    document.addEventListener('visibilitychange', updateVisibility);
    return () => document.removeEventListener('visibilitychange', updateVisibility);
  }, []);

  useEffect(() => {
    const regainedVisibility = documentVisible && !previousDocumentVisibleRef.current;
    previousDocumentVisibleRef.current = documentVisible;
    if (!regainedVisibility || activeView !== 'accounts') return;
    void refreshPassiveAccountsEvidence();
  }, [activeView, documentVisible, refreshPassiveAccountsEvidence]);

  useInterval(
    () => {
      void refreshPassiveAccountsEvidence();
    },
    activeView === 'accounts' &&
      documentVisible &&
      (canLoadHeaderSnapshots || canLoadInspectionSummary)
      ? PASSIVE_ACCOUNTS_EVIDENCE_REFRESH_MS
      : null
  );

  useEffect(
    () => () => {
      if (identityCopyTimerRef.current !== null) {
        clearTimeout(identityCopyTimerRef.current);
      }
    },
    []
  );

  useEffect(() => {
    if (!isAccountSortDropdownOpen || typeof document === 'undefined') return;

    const handleClickOutside = (event: MouseEvent) => {
      const target = event.target as Node;
      if (!accountSortDropdownRef.current?.contains(target)) {
        setIsAccountSortDropdownOpen(false);
      }
    };

    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, [isAccountSortDropdownOpen]);

  useEffect(() => {
    if (!isAccountSortDropdownOpen || highlightedAccountSortIndex < 0) return;
    const highlightedOption = ACCOUNT_SORT_FIELD_OPTIONS[highlightedAccountSortIndex];
    if (!highlightedOption) return;
    accountSortOptionRefs.current.get(highlightedOption.value)?.focus();
  }, [highlightedAccountSortIndex, isAccountSortDropdownOpen]);

  const handleSavePastedAuthJson = useCallback(
    async (type: AuthJsonInputType, fileName: string, jsonText: string) => {
      const saveConnectionFingerprint = connectionFingerprint;
      await savePastedAuthJson(type, fileName, jsonText);
      if (oauthEditorConnectionFingerprintRef.current !== saveConnectionFingerprint) return;
      setAuthJsonPasteOpen(false);
    },
    [connectionFingerprint, savePastedAuthJson]
  );

  const credentialFileNameCounts = useMemo(
    () =>
      files.reduce((counts, file) => {
        counts.set(file.name, (counts.get(file.name) ?? 0) + 1);
        return counts;
      }, new Map<string, number>()),
    [files]
  );

  const buildCurrentCredentialEvidenceBoundary = useCallback(
    ({
      targetFiles,
      inventoryFiles = files,
      fallbackFileNames = [],
      sourceFileNames = [],
      localAtMs = Date.now(),
      authenticationAtMs = 0,
      actionCandidateItems,
      quotaCooldownItems,
    }: {
      targetFiles: AuthFileItem[];
      inventoryFiles?: readonly AuthFileItem[];
      fallbackFileNames?: string[];
      sourceFileNames?: string[];
      localAtMs?: number;
      authenticationAtMs?: number;
      actionCandidateItems?: readonly AccountActionCandidate[];
      quotaCooldownItems?: Iterable<QuotaCooldownInfo>;
    }): AccountCredentialEvidenceBoundary => {
      const currentInspectionSnapshot = inspectionSnapshotRef.current;
      const currentInspectionResults = currentInspectionSnapshot?.results ?? [];
      const inventoryFileNameCounts = inventoryFiles.reduce((counts, file) => {
        counts.set(file.name, (counts.get(file.name) ?? 0) + 1);
        return counts;
      }, new Map<string, number>());
      const currentInspectionBySelectionKey = buildAccountInspectionBySelectionKey(
        [...inventoryFiles],
        currentInspectionResults
      );
      const currentHeaderLookup = buildUsageHeaderSnapshotLookup(headerSnapshots);
      const fallbackNames = new Set(fallbackFileNames.map((name) => name.trim()).filter(Boolean));
      const sourceNames = new Set(sourceFileNames.map((name) => name.trim()).filter(Boolean));
      const targetIdentityKeys = new Set(targetFiles.map(getAuthFileCodexInspectionKeyForFile));
      const uniqueTargetFileNames = new Set(
        targetFiles
          .filter((file) => inventoryFileNameCounts.get(file.name) === 1)
          .map((file) => file.name)
      );
      const getFilenameOnlyIdentityKey = (fileName: string) =>
        getAuthFileCodexInspectionKeyForIdentity({ fileName });
      let inspectionAtMs = 0;
      let headerAtMs = 0;
      let actionAtMs = 0;
      let authenticationActionAtMs = 0;
      let quotaActionAtMs = 0;
      let cooldownAtMs = 0;
      let rawStatusAtMs = 0;
      const rawStatusMessages = new Set<string>();
      const rawStatusCodes = new Set<number>();
      const currentActionCandidates = actionCandidateItems ?? accountActionCandidatesRef.current;
      const currentQuotaCooldowns = quotaCooldownItems ?? quotaCooldownsRef.current.values();

      targetFiles.forEach((file) => {
        const inspection = currentInspectionBySelectionKey.get(getAuthFileSelectionKey(file));
        inspectionAtMs = Math.max(inspectionAtMs, inspection?.createdAtMs ?? 0);
        const headerSnapshot = getHighConfidenceUsageHeaderSnapshotForAuthFile(
          currentHeaderLookup,
          file
        );
        headerAtMs = Math.max(headerAtMs, headerSnapshot?.timestamp_ms ?? 0);
        const rawStatusMessage = readAccountRawStatusMessage(file);
        if (rawStatusMessage) rawStatusMessages.add(rawStatusMessage);
        getAuthFileCredentialStatusCodes(file).forEach((statusCode) =>
          rawStatusCodes.add(statusCode)
        );
        rawStatusAtMs = Math.max(rawStatusAtMs, readAuthFileUpdatedAtMs(file) ?? 0);
      });

      if (fallbackNames.size > 0 || sourceNames.size > 0) {
        currentInspectionResults.forEach((result) => {
          const fileName = result.fileName.trim();
          const identityKey = getAuthFileCodexInspectionKeyForIdentity({
            fileName,
            runtimeId: result.runtimeId,
            provider: result.provider,
            authIndex: result.authIndex,
            accountId: result.accountId,
            accountSnapshot: result.accountSnapshot,
          });
          if (
            !sourceNames.has(fileName) &&
            (!fallbackNames.has(fileName) || identityKey !== getFilenameOnlyIdentityKey(fileName))
          ) {
            return;
          }
          inspectionAtMs = Math.max(inspectionAtMs, result.createdAtMs);
        });
        headerSnapshots.forEach((snapshot) => {
          const fileName = snapshot.auth_file_snapshot?.trim() ?? '';
          const authIndex =
            snapshot.auth_index === null || snapshot.auth_index === undefined
              ? ''
              : String(snapshot.auth_index).trim();
          if (!sourceNames.has(fileName) && (!fallbackNames.has(fileName) || authIndex)) return;
          headerAtMs = Math.max(headerAtMs, snapshot.timestamp_ms);
        });
        inventoryFiles.forEach((file) => {
          const isUniqueFallbackFile =
            fallbackNames.has(file.name) && inventoryFileNameCounts.get(file.name) === 1;
          if (!sourceNames.has(file.name) && !isUniqueFallbackFile) return;
          const rawStatusMessage = readAccountRawStatusMessage(file);
          if (rawStatusMessage) rawStatusMessages.add(rawStatusMessage);
          getAuthFileCredentialStatusCodes(file).forEach((statusCode) =>
            rawStatusCodes.add(statusCode)
          );
          rawStatusAtMs = Math.max(rawStatusAtMs, readAuthFileUpdatedAtMs(file) ?? 0);
        });
      }

      currentActionCandidates.forEach((candidate) => {
        const fileName = candidate.authFileName.trim();
        const identityKey = getAccountOperationalItemIdentityKey(candidate);
        const matchesTarget =
          targetIdentityKeys.has(identityKey) ||
          (uniqueTargetFileNames.has(fileName) &&
            identityKey === getFilenameOnlyIdentityKey(fileName));
        const matchesFallback =
          fallbackNames.has(fileName) && identityKey === getFilenameOnlyIdentityKey(fileName);
        if (!sourceNames.has(fileName) && !matchesTarget && !matchesFallback) return;
        const candidateAtMs = Math.max(
          candidate.updatedAtMs,
          candidate.lastSeenAtMs,
          candidate.createdAtMs
        );
        actionAtMs = Math.max(actionAtMs, candidateAtMs);
        const actionType = candidate.actionType.trim().toLowerCase();
        if (actionType === 'reauth' || actionType === 'delete') {
          authenticationActionAtMs = Math.max(authenticationActionAtMs, candidateAtMs);
        } else if (actionType === 'disable' || actionType === 'enable') {
          quotaActionAtMs = Math.max(quotaActionAtMs, candidateAtMs);
        }
      });
      for (const cooldown of currentQuotaCooldowns) {
        const fileName = cooldown.authFileName.trim();
        const identityKey = getAccountOperationalItemIdentityKey(cooldown);
        const matchesTarget =
          targetIdentityKeys.has(identityKey) ||
          (uniqueTargetFileNames.has(fileName) &&
            identityKey === getFilenameOnlyIdentityKey(fileName));
        const matchesFallback =
          fallbackNames.has(fileName) && identityKey === getFilenameOnlyIdentityKey(fileName);
        if (!sourceNames.has(fileName) && !matchesTarget && !matchesFallback) continue;
        cooldownAtMs = Math.max(
          cooldownAtMs,
          cooldown.disabledAtMs ?? 0,
          cooldown.createdAtMs ?? 0
        );
      }

      return {
        localAtMs,
        inspectionAtMs,
        inspectionBaselinePending: currentInspectionSnapshot === null,
        headerAtMs,
        headerBaselinePending:
          !featureAvailability.checking &&
          featureAvailability.requestMonitoringAvailable &&
          Boolean(featureAvailability.managerServiceBase) &&
          headerSnapshotsLoadedAtMs <= 0,
        actionAtMs,
        actionBaselinePending: managerStorageAvailable && !accountActionCandidatesLoadedRef.current,
        authenticationActionAtMs,
        authenticationActionBaselinePending: false,
        quotaActionAtMs,
        quotaActionBaselinePending: false,
        cooldownAtMs,
        cooldownBaselinePending: managerStorageAvailable && !quotaCooldownsLoadedRef.current,
        fallbackInspectionAtMs: 0,
        fallbackHeaderAtMs: 0,
        fallbackActionAtMs: 0,
        fallbackCooldownAtMs: 0,
        authenticationAtMs,
        rawStatusAtMs,
        rawStatusMessages: Array.from(rawStatusMessages),
        rawStatusCodes: Array.from(rawStatusCodes),
      };
    },
    [
      featureAvailability.checking,
      featureAvailability.managerServiceBase,
      featureAvailability.requestMonitoringAvailable,
      files,
      headerSnapshots,
      headerSnapshotsLoadedAtMs,
      managerStorageAvailable,
    ]
  );

  const invalidateCodexCredentialEvidenceForSourceFiles = useCallback(
    (fileNames: string[]): void => {
      const names = new Set(fileNames.map((name) => name.trim()).filter(Boolean));
      if (names.size === 0) return;
      headerSnapshotRequestGenerationRef.current += 1;
      invalidateInspectionSummaryRequest();
      quotaCooldownRequestIdRef.current += 1;
      accountActionCandidatesReqIdRef.current += 1;
      setAccountActionCandidatesLoading(false);
      const matchedFiles = files.filter((file) => names.has(file.name));
      const invalidatedAtMs = Date.now();
      const boundaryEntries: Array<readonly [string, AccountCredentialEvidenceBoundary]> = [
        ...Array.from(
          names,
          (name) =>
            [
              getCredentialEvidenceSourceFileBoundaryKey(name),
              mirrorAccountCredentialEvidenceBoundaryToFallback(
                buildCurrentCredentialEvidenceBoundary({
                  targetFiles: [],
                  sourceFileNames: [name],
                  localAtMs: invalidatedAtMs,
                })
              ),
            ] as const
        ),
        ...matchedFiles.map(
          (file) =>
            [
              getAuthFileSelectionKey(file),
              buildCurrentCredentialEvidenceBoundary({
                targetFiles: [file],
                localAtMs: invalidatedAtMs,
              }),
            ] as const
        ),
      ];
      setCredentialEvidenceBoundaries((current) =>
        upsertAccountCredentialEvidenceBoundaries(current, boundaryEntries)
      );

      const requestKeys = new Set<string>();
      matchedFiles.forEach((file) => {
        if (normalizeAccountProvider(file) !== CODEX_CONFIG.type) return;
        requestKeys.add(CODEX_CONFIG.getStoreKey?.(file) ?? file.name);
        requestKeys.add(file.name);
      });
      requestKeys.forEach((key) => {
        beginAccountQuotaRequest(quotaRequestVersionsRef.current, `${CODEX_CONFIG.type}:${key}`);
      });
      setCodexQuota((current) => {
        let changed = false;
        const next = { ...current };
        Object.entries(current).forEach(([key, state]) => {
          const scopedName = state.authFileName?.trim() ?? '';
          if (!names.has(key) && (!scopedName || !names.has(scopedName)) && !requestKeys.has(key)) {
            return;
          }
          delete next[key];
          changed = true;
        });
        return changed ? next : current;
      });
    },
    [
      buildCurrentCredentialEvidenceBoundary,
      files,
      invalidateInspectionSummaryRequest,
      setCredentialEvidenceBoundaries,
      setCodexQuota,
    ]
  );

  const invalidateCodexCredentialEvidenceForSelectionKeys = useCallback(
    (selectionKeys: string[]): void => {
      const keys = new Set(selectionKeys.filter((key) => key.length > 0));
      if (keys.size === 0) return;
      headerSnapshotRequestGenerationRef.current += 1;
      invalidateInspectionSummaryRequest();
      quotaCooldownRequestIdRef.current += 1;
      accountActionCandidatesReqIdRef.current += 1;
      setAccountActionCandidatesLoading(false);
      const matchedFiles = files.filter((file) => keys.has(getAuthFileSelectionKey(file)));
      const invalidatedAtMs = Date.now();
      const relevantFileNames = new Set(
        Array.from(keys, getAuthFileNameFromSelectionKey)
          .map((name) => name.trim())
          .filter(Boolean)
      );
      matchedFiles.forEach((file) => relevantFileNames.add(file.name));
      const fallbackBoundariesByFileName = new Map(
        Array.from(relevantFileNames, (fileName) => [
          fileName,
          toFallbackAccountCredentialEvidenceBoundary(
            buildCurrentCredentialEvidenceBoundary({
              targetFiles: [],
              fallbackFileNames: [fileName],
              localAtMs: invalidatedAtMs,
            })
          ),
        ])
      );
      const exactBoundariesBySelectionKey = new Map<string, AccountCredentialEvidenceBoundary>();
      const replacementBoundariesByFileName = new Map<string, AccountCredentialEvidenceBoundary>();
      matchedFiles.forEach((file) => {
        const replacementBoundary = mergeAccountCredentialEvidenceBoundaries(
          buildCurrentCredentialEvidenceBoundary({
            targetFiles: [file],
            localAtMs: invalidatedAtMs,
          }),
          fallbackBoundariesByFileName.get(file.name)
        );
        exactBoundariesBySelectionKey.set(getAuthFileSelectionKey(file), replacementBoundary);
        replacementBoundariesByFileName.set(
          file.name,
          mergeAccountCredentialEvidenceBoundaries(
            replacementBoundariesByFileName.get(file.name),
            replacementBoundary
          )
        );
      });
      setCredentialEvidenceBoundaries((current) =>
        upsertAccountCredentialEvidenceBoundaries(
          current,
          Array.from(keys, (key) => {
            const fileName = getAuthFileNameFromSelectionKey(key).trim();
            return [
              key,
              exactBoundariesBySelectionKey.get(key) ??
                replacementBoundariesByFileName.get(fileName) ??
                fallbackBoundariesByFileName.get(fileName) ??
                EMPTY_ACCOUNT_CREDENTIAL_EVIDENCE_BOUNDARY,
            ] as const;
          })
        )
      );

      const invalidatedSelectionKeys = new Set(
        matchedFiles.map((file) => getAuthFileSelectionKey(file))
      );
      const invalidatedStoreKeys = new Set(
        matchedFiles.map((file) => CODEX_CONFIG.getStoreKey?.(file) ?? file.name)
      );
      const preservedFiles = files.filter(
        (file) =>
          relevantFileNames.has(file.name) &&
          !invalidatedSelectionKeys.has(getAuthFileSelectionKey(file))
      );
      invalidatedStoreKeys.forEach((storeKey) => {
        beginAccountQuotaRequest(
          quotaRequestVersionsRef.current,
          `${CODEX_CONFIG.type}:${storeKey}`
        );
      });
      setCodexQuota((current) =>
        pruneCodexQuotaStatesForCredentialMutation(current, {
          affectedFileNames: relevantFileNames,
          invalidatedStoreKeys,
          preservedFiles,
        })
      );
    },
    [
      buildCurrentCredentialEvidenceBoundary,
      files,
      invalidateInspectionSummaryRequest,
      setCredentialEvidenceBoundaries,
      setCodexQuota,
    ]
  );

  const invalidateCodexCredentialStatusForSelectionKeys = useCallback(
    (
      selectionKeys: string[],
      options: {
        supersedeActionEvidence?: boolean;
        supersedeAuthenticationActionEvidence?: boolean;
        supersedeQuotaActionEvidence?: boolean;
        supersedeCooldownEvidence?: boolean;
      } = {}
    ): void => {
      const keys = new Set(selectionKeys.filter((key) => key.length > 0));
      if (keys.size === 0) return;
      invalidateInspectionSummaryRequest();
      if (
        options.supersedeActionEvidence ||
        options.supersedeAuthenticationActionEvidence ||
        options.supersedeQuotaActionEvidence
      ) {
        accountActionCandidatesReqIdRef.current += 1;
        setAccountActionCandidatesLoading(false);
      }
      if (options.supersedeCooldownEvidence) {
        quotaCooldownRequestIdRef.current += 1;
      }
      const invalidatedAtMs = Date.now();
      const matchedFiles = files.filter((file) => keys.has(getAuthFileSelectionKey(file)));
      const statusBoundariesBySelectionKey = new Map(
        matchedFiles.map((file) => {
          const currentBoundary = buildCurrentCredentialEvidenceBoundary({
            targetFiles: [file],
            localAtMs: invalidatedAtMs,
          });
          return [
            getAuthFileSelectionKey(file),
            {
              ...currentBoundary,
              headerAtMs: 0,
              headerBaselinePending: false,
              actionAtMs: options.supersedeActionEvidence ? currentBoundary.actionAtMs : 0,
              actionBaselinePending:
                options.supersedeActionEvidence === true &&
                currentBoundary.actionBaselinePending === true,
              authenticationActionAtMs: options.supersedeAuthenticationActionEvidence
                ? currentBoundary.authenticationActionAtMs
                : 0,
              authenticationActionBaselinePending:
                options.supersedeAuthenticationActionEvidence === true &&
                currentBoundary.actionBaselinePending === true,
              quotaActionAtMs: options.supersedeQuotaActionEvidence
                ? currentBoundary.quotaActionAtMs
                : 0,
              quotaActionBaselinePending:
                options.supersedeQuotaActionEvidence === true &&
                currentBoundary.actionBaselinePending === true,
              cooldownAtMs: options.supersedeCooldownEvidence ? currentBoundary.cooldownAtMs : 0,
              cooldownBaselinePending:
                options.supersedeCooldownEvidence === true &&
                currentBoundary.cooldownBaselinePending === true,
              fallbackInspectionAtMs: 0,
              fallbackInspectionBaselinePending: false,
              fallbackHeaderAtMs: 0,
              fallbackHeaderBaselinePending: false,
              fallbackActionAtMs: 0,
              fallbackActionBaselinePending: false,
              fallbackCooldownAtMs: 0,
              fallbackCooldownBaselinePending: false,
            },
          ] as const;
        })
      );
      setCredentialStatusBoundaries((current) => {
        const next = new Map(current);
        keys.forEach((key) => {
          const statusBoundary =
            statusBoundariesBySelectionKey.get(key) ?? EMPTY_ACCOUNT_CREDENTIAL_EVIDENCE_BOUNDARY;
          next.set(key, {
            ...mergeAccountCredentialEvidenceBoundaries(current.get(key), statusBoundary),
            rawStatusAtMs: statusBoundary.rawStatusAtMs,
            rawStatusMessages: statusBoundary.rawStatusMessages,
            rawStatusCodes: statusBoundary.rawStatusCodes,
          });
        });
        return next;
      });
    },
    [
      buildCurrentCredentialEvidenceBoundary,
      files,
      invalidateInspectionSummaryRequest,
      setCredentialStatusBoundaries,
    ]
  );

  const invalidateProviderCredentialEvidence = useCallback(
    (
      provider: string,
      createdAtMs: number,
      options: {
        credentialFiles?: readonly AuthFileItem[];
        inventoryFiles?: readonly AuthFileItem[];
        supersedeRequests?: boolean;
        scope?: 'provider' | 'credential';
      } = {}
    ): AuthFileItem[] => {
      const normalizedProvider = provider.trim().toLowerCase().replace(/_/g, '-');
      if (!normalizedProvider) return [];
      const scope = options.scope ?? 'provider';
      const credentialFiles = options.credentialFiles ?? files;
      const inventoryFiles = options.inventoryFiles ?? files;
      const supersedeRequests = options.supersedeRequests !== false;
      const targetFiles =
        scope === 'credential' && !options.credentialFiles
          ? []
          : credentialFiles.filter((file) => normalizeAccountProvider(file) === normalizedProvider);
      if (targetFiles.length === 0) return [];
      const targetSelectionKeys = new Set(targetFiles.map((file) => getAuthFileSelectionKey(file)));
      const preservedFiles =
        scope === 'credential'
          ? inventoryFiles.filter(
              (file) =>
                normalizeAccountProvider(file) === normalizedProvider &&
                !targetSelectionKeys.has(getAuthFileSelectionKey(file))
            )
          : [];
      const providerBoundary = buildProviderCredentialMutationBoundary(createdAtMs);
      const rawStatusBoundaryEntries = targetFiles.flatMap((file) => {
        const boundary = buildCredentialMutationRawStatusBoundary(file, createdAtMs);
        return boundary ? ([[getAuthFileSelectionKey(file), boundary]] as const) : [];
      });
      const credentialBoundaryEntries =
        scope === 'credential'
          ? targetFiles.map((file) => [getAuthFileSelectionKey(file), providerBoundary] as const)
          : [];
      const boundaryEntries: Array<readonly [string, AccountCredentialEvidenceBoundary]> = [
        ...(scope === 'provider'
          ? [
              [
                getCredentialEvidenceProviderBoundaryKey(normalizedProvider),
                providerBoundary,
              ] as const,
            ]
          : credentialBoundaryEntries),
        ...rawStatusBoundaryEntries,
      ];
      setCredentialEvidenceBoundaries((current) =>
        upsertAccountCredentialEvidenceBoundaries(current, boundaryEntries)
      );

      if (supersedeRequests && scope === 'provider') {
        headerSnapshotRequestGenerationRef.current += 1;
        invalidateInspectionSummaryRequest();
        accountActionCandidatesReqIdRef.current += 1;
        quotaCooldownRequestIdRef.current += 1;
        setAccountActionCandidatesLoading(false);
      }

      if (normalizedProvider === CODEX_CONFIG.type) {
        const affectedFileNames = new Set(targetFiles.map((file) => file.name));
        const invalidatedStoreKeys = new Set(
          targetFiles.map((file) => CODEX_CONFIG.getStoreKey?.(file) ?? file.name)
        );
        if (supersedeRequests) {
          invalidatedStoreKeys.forEach((storeKey) => {
            beginAccountQuotaRequest(
              quotaRequestVersionsRef.current,
              `${CODEX_CONFIG.type}:${storeKey}`
            );
          });
        }
        setCodexQuota((current) =>
          pruneCodexQuotaStatesForCredentialMutation(current, {
            affectedFileNames,
            invalidatedStoreKeys,
            preservedFiles,
            preserveEvidenceAfterMs: createdAtMs,
          })
        );
        return targetFiles;
      }

      const prune = <
        TState extends {
          authFileName?: string;
          authFileKey?: string;
          fetchedAtMs?: number;
          failedAtMs?: number;
        },
        TData,
      >(
        config: QuotaConfig<TState, TData>,
        setQuota: QuotaSetter<TState>
      ) => {
        if (supersedeRequests) {
          targetFiles.forEach((file) => {
            const storeKey = config.getStoreKey?.(file) ?? file.name;
            beginAccountQuotaRequest(quotaRequestVersionsRef.current, `${config.type}:${storeKey}`);
          });
        }
        setQuota((current) =>
          pruneCredentialQuotaStatesForProviderMutation(
            current,
            targetFiles,
            (file) => config.getStoreKey?.(file) ?? file.name,
            createdAtMs,
            preservedFiles
          )
        );
      };
      switch (normalizedProvider) {
        case CLAUDE_CONFIG.type:
          prune(CLAUDE_CONFIG, setClaudeQuota);
          break;
        case ANTIGRAVITY_CONFIG.type:
          prune(ANTIGRAVITY_CONFIG, setAntigravityQuota);
          break;
        case KIMI_CONFIG.type:
          prune(KIMI_CONFIG, setKimiQuota);
          break;
        case XAI_CONFIG.type:
          prune(XAI_CONFIG, setXaiQuota);
          break;
        default:
          break;
      }
      return targetFiles;
    },
    [
      files,
      invalidateInspectionSummaryRequest,
      setAntigravityQuota,
      setClaudeQuota,
      setCredentialEvidenceBoundaries,
      setCodexQuota,
      setKimiQuota,
      setXaiQuota,
    ]
  );
  invalidateProviderCredentialEvidenceRef.current = invalidateProviderCredentialEvidence;

  const capturePendingInspectionEvidenceBaseline = useCallback(
    (snapshot: CredentialInspectionSnapshot): void => {
      const index = buildAccountCredentialEvidenceBaselineIndex(
        files,
        snapshot.results.map((result) => ({
          fileName: result.fileName,
          identityKey: getAuthFileCodexInspectionKeyForIdentity({
            fileName: result.fileName,
            runtimeId: result.runtimeId,
            provider: result.provider,
            authIndex: result.authIndex,
            accountId: result.accountId,
            accountSnapshot: result.accountSnapshot,
          }),
          atMs: result.createdAtMs,
        })),
        snapshot.completedAtMs
      );
      const capture = (current: Map<string, AccountCredentialEvidenceBoundary>) =>
        capturePendingAccountCredentialEvidenceBaseline(current, 'inspection', index);
      setCredentialEvidenceBoundaries(capture);
      setCredentialStatusBoundaries(capture);
    },
    [files, setCredentialEvidenceBoundaries, setCredentialStatusBoundaries]
  );
  capturePendingInspectionEvidenceBaselineRef.current = capturePendingInspectionEvidenceBaseline;

  const capturePendingHeaderEvidenceBaseline = useCallback(
    (response: UsageHeaderSnapshotsResponse): void => {
      const baseIndex = buildAccountCredentialEvidenceBaselineIndex(
        files,
        response.items.map((item) => {
          const fileName = item.auth_file_snapshot?.trim() ?? '';
          return {
            fileName,
            identityKey: getAuthFileCodexInspectionKeyForIdentity({
              fileName,
              provider: item.auth_provider_snapshot,
              authIndex: item.auth_index,
              accountSnapshot: item.account_snapshot,
            }),
            atMs: item.timestamp_ms,
          };
        }),
        Math.max(response.generated_at_ms, response.to_ms)
      );
      const exactBySelectionKey = new Map(baseIndex.exactBySelectionKey);
      const lookup = buildUsageHeaderSnapshotLookup(response.items);
      files.forEach((file) => {
        const item = getHighConfidenceUsageHeaderSnapshotForAuthFile(lookup, file);
        if (!item) return;
        updateCredentialEvidenceTimestamp(
          exactBySelectionKey,
          getAuthFileSelectionKey(file),
          item.timestamp_ms
        );
      });
      const index = { ...baseIndex, exactBySelectionKey };
      const capture = (current: Map<string, AccountCredentialEvidenceBoundary>) =>
        capturePendingAccountCredentialEvidenceBaseline(current, 'header', index);
      setCredentialEvidenceBoundaries(capture);
      setCredentialStatusBoundaries(capture);
    },
    [files, setCredentialEvidenceBoundaries, setCredentialStatusBoundaries]
  );
  capturePendingHeaderEvidenceBaselineRef.current = capturePendingHeaderEvidenceBaseline;

  const capturePendingActionEvidenceBaseline = useCallback(
    (items: readonly AccountActionCandidate[]): void => {
      const toTimestampItems = (source: readonly AccountActionCandidate[]) =>
        source.map((item) => ({
          fileName: item.authFileName,
          identityKey: getAccountOperationalItemIdentityKey(item),
          atMs: Math.max(item.updatedAtMs, item.lastSeenAtMs, item.createdAtMs),
        }));
      const index = buildAccountCredentialEvidenceBaselineIndex(files, toTimestampItems(items));
      const authenticationIndex = buildAccountCredentialEvidenceBaselineIndex(
        files,
        toTimestampItems(
          items.filter((item) =>
            ['reauth', 'delete'].includes(item.actionType.trim().toLowerCase())
          )
        )
      );
      const quotaIndex = buildAccountCredentialEvidenceBaselineIndex(
        files,
        toTimestampItems(
          items.filter((item) =>
            ['disable', 'enable'].includes(item.actionType.trim().toLowerCase())
          )
        )
      );
      const capture = (current: Map<string, AccountCredentialEvidenceBoundary>) =>
        capturePendingAccountCredentialEvidenceBaseline(current, 'action', index);
      setCredentialEvidenceBoundaries(capture);
      setCredentialStatusBoundaries((current) => {
        const captured = capture(current);
        let next: Map<string, AccountCredentialEvidenceBoundary> | null = null;
        captured.forEach((boundary, key) => {
          if (
            boundary.authenticationActionBaselinePending !== true &&
            boundary.quotaActionBaselinePending !== true
          ) {
            return;
          }
          const updated = { ...boundary };
          if (boundary.authenticationActionBaselinePending === true) {
            const baseline = resolveAccountCredentialEvidenceBaseline(key, authenticationIndex);
            updated.authenticationActionAtMs = Math.max(
              updated.authenticationActionAtMs,
              baseline.atMs,
              baseline.fallbackAtMs
            );
            updated.authenticationActionBaselinePending = false;
          }
          if (boundary.quotaActionBaselinePending === true) {
            const baseline = resolveAccountCredentialEvidenceBaseline(key, quotaIndex);
            updated.quotaActionAtMs = Math.max(
              updated.quotaActionAtMs,
              baseline.atMs,
              baseline.fallbackAtMs
            );
            updated.quotaActionBaselinePending = false;
          }
          if (!next) next = new Map(captured);
          next.set(key, updated);
        });
        return next ?? captured;
      });
    },
    [files, setCredentialEvidenceBoundaries, setCredentialStatusBoundaries]
  );
  capturePendingActionEvidenceBaselineRef.current = capturePendingActionEvidenceBaseline;

  const capturePendingCooldownEvidenceBaseline = useCallback(
    (items: readonly QuotaCooldownInfo[]): void => {
      const index = buildAccountCredentialEvidenceBaselineIndex(
        files,
        items.map((item) => ({
          fileName: item.authFileName,
          identityKey: getAccountOperationalItemIdentityKey(item),
          atMs: Math.max(item.disabledAtMs ?? 0, item.createdAtMs ?? 0),
        }))
      );
      const capture = (current: Map<string, AccountCredentialEvidenceBoundary>) =>
        capturePendingAccountCredentialEvidenceBaseline(current, 'cooldown', index);
      setCredentialEvidenceBoundaries(capture);
      setCredentialStatusBoundaries(capture);
    },
    [files, setCredentialEvidenceBoundaries, setCredentialStatusBoundaries]
  );
  capturePendingCooldownEvidenceBaselineRef.current = capturePendingCooldownEvidenceBaseline;

  useLayoutEffect(() => {
    if (inspectionSnapshot) capturePendingInspectionEvidenceBaseline(inspectionSnapshot);
  }, [capturePendingInspectionEvidenceBaseline, inspectionSnapshot]);

  credentialMutationHandlerRef.current = (mutation) => {
    if (mutation.kind === 'source-files-changed') {
      invalidateCodexCredentialEvidenceForSourceFiles(mutation.fileNames);
      return;
    }
    if (mutation.kind === 'credential-refreshed') {
      invalidateCodexCredentialEvidenceForSelectionKeys(mutation.selectionKeys);
      publishAccountCredentialMutationRevision({
        connectionFingerprint,
        provider: 'codex',
        kind: 'credential',
        credentialIdentity: mutation.selectionKeys[0],
      });
      return;
    }
    invalidateCodexCredentialStatusForSelectionKeys(mutation.selectionKeys);
  };

  const invalidateCodexCredentialEvidenceForMatchedFile = useCallback(
    (
      baseline: AccountDirectReauthBaseline,
      matchedFile: AuthFileItem,
      inventoryFiles: readonly AuthFileItem[],
      options: { authenticationAtMs?: number; invalidatedAtMs?: number } = {}
    ): CodexCredentialEvidenceInvalidation => {
      headerSnapshotRequestGenerationRef.current += 1;
      invalidateInspectionSummaryRequest();
      quotaCooldownRequestIdRef.current += 1;
      accountActionCandidatesReqIdRef.current += 1;
      setAccountActionCandidatesLoading(false);
      const selectionKey = getAuthFileSelectionKey(matchedFile);
      const boundaryKeys = [
        selectionKey,
        getCredentialEvidenceUniqueFileNameBoundaryKey(matchedFile.name),
      ];
      const invalidatedAtMs = options.invalidatedAtMs ?? Date.now();
      const authenticationAtMs = options.authenticationAtMs ?? 0;
      const canUseCredentialAuthenticationFallback =
        inventoryFiles.filter((file) => file.name === matchedFile.name).length === 1;
      const selectionBoundary = keepAuthenticationRecoveryBoundary(
        buildCurrentCredentialEvidenceBoundary({
          targetFiles: [matchedFile],
          inventoryFiles,
          localAtMs: invalidatedAtMs,
          authenticationAtMs,
        })
      );
      const uniqueFileNameEvidence = buildCurrentCredentialEvidenceBoundary({
        targetFiles: [],
        inventoryFiles,
        fallbackFileNames: [matchedFile.name],
        localAtMs: invalidatedAtMs,
        authenticationAtMs: canUseCredentialAuthenticationFallback ? authenticationAtMs : 0,
      });
      const uniqueFileNameBoundary =
        toFallbackAccountCredentialEvidenceBoundary(uniqueFileNameEvidence);
      setCredentialEvidenceBoundaries((current) =>
        upsertAccountCredentialEvidenceBoundaries(current, [
          [boundaryKeys[0], selectionBoundary],
          [boundaryKeys[1], uniqueFileNameBoundary],
        ])
      );

      const storeKey = CODEX_CONFIG.getStoreKey?.(matchedFile) ?? matchedFile.name;
      const previousStoreKey = getBaselineCodexQuotaStoreKey(baseline);
      new Set([previousStoreKey, storeKey]).forEach((key) => {
        if (key) {
          beginAccountQuotaRequest(quotaRequestVersionsRef.current, `${CODEX_CONFIG.type}:${key}`);
        }
      });
      const preservedFiles = inventoryFiles.filter((file) => file.name === matchedFile.name);
      setCodexQuota((current) => {
        const migrated = migrateConfirmedReauthCodexQuotaState(current, {
          baseline,
          confirmedFile: matchedFile,
          inventoryFiles,
          authenticationAtMs,
        });
        const sanitized = Object.entries(migrated).reduce<Record<string, CodexQuotaState>>(
          (next, [key, state]) => {
            if (
              authenticationAtMs <= 0 ||
              getAuthFileScopedCodexQuota(matchedFile, state) !== state
            ) {
              next[key] = state;
              return next;
            }
            next[key] =
              sanitizeSupersededAuthQuotaState(state, authenticationAtMs, {
                allowUnknownFailureTimestamp: true,
              }) ?? state;
            return next;
          },
          {}
        );
        return pruneCodexQuotaStatesForCredentialMutation(sanitized, {
          affectedFileNames: new Set([matchedFile.name]),
          preservedFiles,
        });
      });
      return { file: matchedFile, invalidatedAtMs };
    },
    [
      buildCurrentCredentialEvidenceBoundary,
      invalidateInspectionSummaryRequest,
      setCredentialEvidenceBoundaries,
      setCodexQuota,
    ]
  );

  const captureReloadedCodexOperationalEvidence = useCallback(
    (
      invalidation: CodexCredentialEvidenceInvalidation | null,
      inventoryFiles: readonly AuthFileItem[] = files
    ): void => {
      if (!invalidation) return;
      const { file, invalidatedAtMs } = invalidation;
      const actionCandidateItems = accountActionCandidatesRef.current;
      const quotaCooldownItems = Array.from(quotaCooldownsRef.current.values());
      const exactEvidence = buildCurrentCredentialEvidenceBoundary({
        targetFiles: [file],
        inventoryFiles,
        localAtMs: invalidatedAtMs,
        actionCandidateItems,
        quotaCooldownItems,
      });
      const fallbackEvidence = buildCurrentCredentialEvidenceBoundary({
        targetFiles: [],
        inventoryFiles,
        fallbackFileNames: [file.name],
        localAtMs: invalidatedAtMs,
        actionCandidateItems,
        quotaCooldownItems,
      });
      const exactBoundary: AccountCredentialEvidenceBoundary = {
        ...EMPTY_ACCOUNT_CREDENTIAL_EVIDENCE_BOUNDARY,
        actionAtMs: exactEvidence.actionAtMs,
        cooldownAtMs: exactEvidence.cooldownAtMs,
      };
      const fallbackBoundary: AccountCredentialEvidenceBoundary = {
        ...EMPTY_ACCOUNT_CREDENTIAL_EVIDENCE_BOUNDARY,
        fallbackActionAtMs: fallbackEvidence.actionAtMs,
        fallbackCooldownAtMs: fallbackEvidence.cooldownAtMs,
      };
      setCredentialEvidenceBoundaries((current) =>
        upsertAccountCredentialEvidenceBoundaries(current, [
          [getAuthFileSelectionKey(file), exactBoundary],
          [getCredentialEvidenceUniqueFileNameBoundaryKey(file.name), fallbackBoundary],
        ])
      );
    },
    [buildCurrentCredentialEvidenceBoundary, files, setCredentialEvidenceBoundaries]
  );

  const completeConfirmedAccountDirectReauth = useCallback(
    (
      baseline: AccountDirectReauthBaseline,
      confirmedFile: AuthFileItem,
      inventoryFiles: readonly AuthFileItem[],
      pendingId?: string
    ): void => {
      const completedAtMs = Date.now();
      if (baseline.resultKeys.length > 0) {
        recordCompletedAccountReauthSession({
          connectionFingerprint,
          oauthProvider: 'codex',
          resultKeys: baseline.resultKeys,
          completedAtMs,
        });
        setSuppressedInspectionResultKeys((current) => {
          const next = new Set(current);
          baseline.resultKeys.forEach((key) => next.add(key));
          return next;
        });
      }
      if (pendingId) acknowledgePendingAccountDirectReauths([pendingId]);
      const invalidation = invalidateCodexCredentialEvidenceForMatchedFile(
        baseline,
        confirmedFile,
        inventoryFiles,
        { authenticationAtMs: completedAtMs, invalidatedAtMs: completedAtMs }
      );
      captureReloadedCodexOperationalEvidence(invalidation, inventoryFiles);
      publishAccountCredentialMutationRevision({
        connectionFingerprint,
        provider: 'codex',
        kind: 'reauth',
        credentialIdentity: getAuthFileSelectionKey(confirmedFile),
      });
    },
    [
      captureReloadedCodexOperationalEvidence,
      connectionFingerprint,
      invalidateCodexCredentialEvidenceForMatchedFile,
    ]
  );

  const reconcilePendingAccountDirectReauthsWithRetry = useCallback(
    async (
      pendingItems: readonly PendingAccountDirectReauth[],
      options: { reload?: boolean } = {}
    ): Promise<AccountDirectReauthReconciliationBatch | null> => {
      if (pendingItems.length === 0) return { files, reconciliations: new Map() };
      const synchronizationScopeKey = credentialEvidenceScopeKey;
      let firstAttempt = true;
      const retry = await runCredentialVisibilityRetry<AuthFileItem[]>({
        load: async () => {
          const loadedFiles =
            firstAttempt && options.reload === false
              ? files
              : firstAttempt
                ? await reloadInspectionCredentialArtifacts({ requireSuccessfulReload: true })
                : await loadFiles({ throwOnError: true });
          firstAttempt = false;
          if (!loadedFiles) throw new Error(t('notification.refresh_failed'));
          return loadedFiles;
        },
        isUnconfirmed: (loadedFiles) =>
          pendingItems.some(
            (pending) =>
              Boolean(pending.target.accountId) &&
              reconcileAccountDirectReauth(pending, loadedFiles).status === 'unconfirmed'
          ),
        isActive: () =>
          isMountedRef.current &&
          activeCredentialEvidenceScopeKeyRef.current === synchronizationScopeKey,
      });
      if (
        retry.cancelled ||
        !isMountedRef.current ||
        activeCredentialEvidenceScopeKeyRef.current !== synchronizationScopeKey
      ) {
        return null;
      }
      if (retry.error) {
        throw retry.error;
      }
      const reconciliations = new Map<string, AccountDirectReauthReconciliation>();
      pendingItems.forEach((pending) => {
        reconciliations.set(
          pending.id,
          retry.value
            ? reconcileAccountDirectReauth(pending, retry.value)
            : { status: 'unconfirmed' }
        );
      });
      return { files: retry.value ?? files, reconciliations };
    },
    [credentialEvidenceScopeKey, files, loadFiles, reloadInspectionCredentialArtifacts, t]
  );

  const synchronizePendingAccountDirectReauths = useCallback(
    async (options: { reload?: boolean } = {}): Promise<boolean> => {
      const pendingItems = listPendingAccountDirectReauths(connectionFingerprint);
      if (pendingItems.length === 0) return false;
      const synchronizationScopeKey = credentialEvidenceScopeKey;
      const existing = directReauthSynchronizationsRef.current.get(synchronizationScopeKey);
      if (existing) return existing;

      const synchronization = (async () => {
        try {
          const reconciliationBatch = await reconcilePendingAccountDirectReauthsWithRetry(
            pendingItems,
            options
          );
          if (!reconciliationBatch) return false;

          let synchronized = false;
          pendingItems.forEach((pending) => {
            const reconciliation = reconciliationBatch.reconciliations.get(pending.id) ?? {
              status: 'unconfirmed',
            };
            if (reconciliation.status === 'confirmed') {
              completeConfirmedAccountDirectReauth(
                pending,
                reconciliation.file,
                reconciliationBatch.files,
                pending.id
              );
              synchronized = true;
              return;
            }
            if (
              reconciliation.status === 'identity-changed' ||
              reconciliation.status === 'ambiguous'
            ) {
              acknowledgePendingAccountDirectReauths([pending.id]);
              showNotification(
                t(
                  reconciliation.status === 'identity-changed'
                    ? 'codex_reauth.identity_changed'
                    : 'codex_reauth.identity_ambiguous'
                ),
                'warning'
              );
            }
          });
          return synchronized;
        } catch {
          return false;
        } finally {
          directReauthSynchronizationsRef.current.delete(synchronizationScopeKey);
        }
      })();
      directReauthSynchronizationsRef.current.set(synchronizationScopeKey, synchronization);
      return synchronization;
    },
    [
      completeConfirmedAccountDirectReauth,
      connectionFingerprint,
      credentialEvidenceScopeKey,
      reconcilePendingAccountDirectReauthsWithRetry,
      showNotification,
      t,
    ]
  );
  synchronizePendingAccountDirectReauthsRef.current = synchronizePendingAccountDirectReauths;

  const handleCodexReauthSuccess = useCallback(async () => {
    const baseline = codexReauthBaselineRef.current;
    if (!baseline) throw new Error(t('notification.refresh_failed'));
    const pending = recordPendingAccountDirectReauth({ connectionFingerprint, baseline });
    if (!pending) throw new Error(t('notification.refresh_failed'));
    const reconciliationBatch = await reconcilePendingAccountDirectReauthsWithRetry([pending], {
      reload: true,
    });
    if (!reconciliationBatch) return;
    const reconciliation =
      reconciliationBatch.reconciliations.get(pending.id) ?? ({ status: 'unconfirmed' } as const);
    if (reconciliation.status === 'confirmed') {
      completeConfirmedAccountDirectReauth(
        pending,
        reconciliation.file,
        reconciliationBatch.files,
        pending.id
      );
      return;
    }
    if (reconciliation.status !== 'unconfirmed') {
      acknowledgePendingAccountDirectReauths([pending.id]);
    }
    const code =
      reconciliation.status === 'identity-changed'
        ? 'identity_changed'
        : reconciliation.status === 'ambiguous'
          ? 'identity_ambiguous'
          : 'identity_unconfirmed';
    throw new CodexReauthReconciliationError(code, t(`codex_reauth.${code}`));
  }, [
    completeConfirmedAccountDirectReauth,
    connectionFingerprint,
    reconcilePendingAccountDirectReauthsWithRetry,
    t,
  ]);

  const handleReauthAccount = useCallback(
    (file: AuthFileItem) => {
      const action = resolveAccountReauthAction(file);
      if (action.kind === 'codex-dialog') {
        const target = createCodexReauthTargetFromAuthFile(file);
        const targetIdentityKey = getAuthFileCodexInspectionKeyForFile(file);
        const baseline = createAccountDirectReauthBaseline({
          target,
          file,
          files,
          resultKeys: getHandledAccountInspectionResultKeys(
            inspectionResults,
            targetIdentityKey,
            file.name,
            files
          ),
        });
        if (!baseline) {
          showNotification(t('notification.refresh_failed'), 'error');
          return;
        }
        codexReauthBaselineRef.current = baseline;
        setCodexReauthTarget(target);
        return;
      }
      if (action.kind === 'navigate') {
        const targetIdentityKey = getAuthFileCodexInspectionKeyForFile(file);
        const handledResultKeys = getHandledAccountInspectionResultKeys(
          inspectionResults,
          targetIdentityKey,
          file.name,
          files
        );
        const sessionId = beginAccountOAuthReauthSession({
          connectionFingerprint,
          oauthProvider: action.oauthProvider,
          resultKeys: handledResultKeys,
        });
        navigate(
          sessionId ? buildAccountOAuthReauthPath(action.oauthProvider, sessionId) : action.path
        );
        return;
      }
      showNotification(
        t('accounts.reauth_unsupported', {
          defaultValue: '该 Provider 暂不支持从凭证工作区重新认证：{{provider}}',
          provider: action.provider,
        }),
        'info'
      );
    },
    [connectionFingerprint, files, inspectionResults, navigate, showNotification, t]
  );

  const requestEvidenceBySelectionKey = useMemo(() => {
    const evidenceMap = new Map<string, AccountRequestEvidenceInput>();
    accountHistoryByRowKey.forEach((history, selectionKey) => {
      evidenceMap.set(selectionKey, {
        latestRequest: history.latest_request ?? null,
        recentRequests: history.recent_requests ?? [],
      });
    });
    return evidenceMap;
  }, [accountHistoryByRowKey]);
  const headerSnapshotLookup = useMemo(
    () => buildUsageHeaderSnapshotLookup(headerSnapshots),
    [headerSnapshots]
  );
  const credentialEvidenceBoundariesBySelectionKey = useMemo(() => {
    const boundaries = new Map<string, AccountCredentialEvidenceBoundary>();
    files.forEach((file) => {
      boundaries.set(
        getAuthFileSelectionKey(file),
        getCredentialEvidenceBoundaryForFile(
          file,
          credentialEvidenceBoundaries,
          credentialFileNameCounts
        )
      );
    });
    return boundaries;
  }, [credentialEvidenceBoundaries, credentialFileNameCounts, files]);
  const effectiveInspectionResults = useMemo(
    () =>
      filterSuppressedAccountInspectionResults(inspectionResults, suppressedInspectionResultKeys),
    [inspectionResults, suppressedInspectionResultKeys]
  );
  const accountInspectionBySelectionKey = useMemo(
    () =>
      buildAccountInspectionBySelectionKey(
        files,
        effectiveInspectionResults,
        credentialEvidenceBoundariesBySelectionKey
      ),
    [credentialEvidenceBoundariesBySelectionKey, effectiveInspectionResults, files]
  );
  const getActiveCodexQuota = useCallback(
    (file: AuthFileItem): CodexQuotaState | undefined => {
      if (normalizeAccountProvider(file) !== CODEX_CONFIG.type) return undefined;
      const storeKey = CODEX_CONFIG.getStoreKey?.(file) ?? file.name;
      return (
        getAuthFileScopedCodexQuota(file, codexQuota[storeKey]) ??
        getAuthFileScopedCodexQuota(file, codexQuota[file.name])
      );
    },
    [codexQuota]
  );
  const getCredentialEvidenceBoundary = useCallback(
    (file: AuthFileItem): AccountCredentialEvidenceBoundary =>
      getCredentialEvidenceBoundaryForFile(
        file,
        credentialEvidenceBoundaries,
        credentialFileNameCounts
      ),
    [credentialEvidenceBoundaries, credentialFileNameCounts]
  );
  const getFreshCodexHeaderSnapshot = useCallback(
    (file: AuthFileItem): UsageHeaderSnapshot | undefined => {
      const headerSnapshot = getHighConfidenceUsageHeaderSnapshotForAuthFile(
        headerSnapshotLookup,
        file
      );
      if (isUsageHeaderQuotaSnapshotExpired(headerSnapshot, headerSnapshotGeneratedAtMs)) {
        return undefined;
      }
      const evidenceBoundary = getCredentialEvidenceBoundary(file);
      const usesFileNameFallback =
        normalizeAuthIndex(file['auth_index'] ?? file.authIndex) === null;
      if (
        evidenceBoundary.headerBaselinePending === true ||
        (usesFileNameFallback && evidenceBoundary.fallbackHeaderBaselinePending === true)
      ) {
        return undefined;
      }
      const headerBoundaryAtMs = Math.max(
        evidenceBoundary.headerAtMs,
        usesFileNameFallback ? evidenceBoundary.fallbackHeaderAtMs : 0
      );
      if (!headerSnapshot || headerSnapshot.timestamp_ms <= headerBoundaryAtMs) {
        return undefined;
      }
      const credentialRefreshAtMs = readAuthFileCredentialRefreshAtMs(file) ?? 0;
      if (
        credentialRefreshAtMs > 0 &&
        headerSnapshot.timestamp_ms <= credentialRefreshAtMs &&
        isObservedCodexAuthenticationError(
          getHeaderSnapshotErrorKind(headerSnapshot),
          getHeaderSnapshotErrorCode(headerSnapshot)
        )
      ) {
        return sanitizeSupersededAuthHeaderSnapshot(headerSnapshot, credentialRefreshAtMs);
      }
      return headerSnapshot;
    },
    [getCredentialEvidenceBoundary, headerSnapshotGeneratedAtMs, headerSnapshotLookup]
  );
  const getLatestPositiveRequestAtMs = useCallback(
    (file: AuthFileItem): number | null => {
      const requestEvidence = resolveAccountRequestHealthEvidence(
        requestEvidenceBySelectionKey.get(getAuthFileSelectionKey(file))
      );
      const requestCredentialEvidence = getAccountRequestCredentialEvidence(requestEvidence);
      return requestCredentialEvidence?.direction === 'positive'
        ? requestCredentialEvidence.request.timestamp_ms
        : null;
    },
    [requestEvidenceBySelectionKey]
  );
  const getEffectiveCodexHeaderSnapshot = useCallback(
    (file: AuthFileItem): UsageHeaderSnapshot | undefined => {
      const selectionKey = getAuthFileSelectionKey(file);
      const authenticationBoundaryAtMs = Math.max(
        getCredentialEvidenceBoundary(file).authenticationAtMs,
        credentialStatusBoundaries.get(selectionKey)?.authenticationAtMs ?? 0
      );
      const authenticationAtMs = Math.max(
        getAccountCredentialEvidenceCutoffs({
          providerQuota: getActiveCodexQuota(file),
          inspection: accountInspectionBySelectionKey.get(selectionKey),
          authenticationBoundaryAtMs,
          credentialRefreshAtMs: readAuthFileCredentialRefreshAtMs(file) ?? 0,
        }).authenticationAtMs,
        getLatestPositiveRequestAtMs(file) ?? 0
      );
      return sanitizeSupersededAuthHeaderSnapshot(
        getFreshCodexHeaderSnapshot(file),
        authenticationAtMs > 0 ? authenticationAtMs : undefined
      );
    },
    [
      accountInspectionBySelectionKey,
      credentialStatusBoundaries,
      getActiveCodexQuota,
      getCredentialEvidenceBoundary,
      getFreshCodexHeaderSnapshot,
      getLatestPositiveRequestAtMs,
    ]
  );
  const getDisplayCodexHeaderSnapshot = useCallback(
    (file: AuthFileItem): UsageHeaderSnapshot | undefined => {
      const activeQuota = getActiveCodexQuota(file);
      return getFreshAuthFileCodexStatusSources(
        file,
        activeQuota,
        undefined,
        getEffectiveCodexHeaderSnapshot(file),
        getLatestPositiveRequestAtMs(file),
        headerSnapshotGeneratedAtMs
      ).headerSnapshot;
    },
    [
      getActiveCodexQuota,
      getEffectiveCodexHeaderSnapshot,
      getLatestPositiveRequestAtMs,
      headerSnapshotGeneratedAtMs,
    ]
  );
  const getFreshCodexHeaderQuota = useCallback(
    (file: AuthFileItem): CodexQuotaState | undefined =>
      buildObservedCodexQuotaState(
        file,
        getDisplayCodexHeaderSnapshot(file),
        t,
        headerSnapshotGeneratedAtMs
      ),
    [getDisplayCodexHeaderSnapshot, headerSnapshotGeneratedAtMs, t]
  );
  const getDisplayCodexQuota = useCallback(
    (file: AuthFileItem): CodexQuotaState | undefined => {
      if (normalizeAccountProvider(file) !== CODEX_CONFIG.type) return undefined;
      const selectionKey = getAuthFileSelectionKey(file);
      const headerQuota = getFreshCodexHeaderQuota(file);
      const inspection = accountInspectionBySelectionKey.get(selectionKey);
      const inspectionQuota = buildInspectionCodexQuotaState(
        file,
        inspection
          ? stripSupersededAccountInspectionStatus(
              inspection,
              credentialStatusBoundaries.get(selectionKey)?.inspectionAtMs ?? 0
            )
          : undefined
      );
      const boundary = getCredentialEvidenceBoundary(file);
      const authenticationBoundaryAtMs = Math.max(
        boundary.authenticationAtMs,
        credentialStatusBoundaries.get(selectionKey)?.authenticationAtMs ?? 0
      );
      const reconciled = reconcileCodexQuotaEvidence({
        providerQuota: getActiveCodexQuota(file),
        headerQuota,
        inspectionQuota,
        authenticationBoundaryAtMs,
        credentialRefreshAtMs: readAuthFileCredentialRefreshAtMs(file) ?? 0,
      });
      return (
        reconciled ??
        (boundary.localAtMs > 0 ||
        boundary.inspectionAtMs > 0 ||
        boundary.headerAtMs > 0 ||
        authenticationBoundaryAtMs > 0
          ? { status: 'idle', windows: [] }
          : undefined)
      );
    },
    [
      accountInspectionBySelectionKey,
      credentialStatusBoundaries,
      getActiveCodexQuota,
      getCredentialEvidenceBoundary,
      getFreshCodexHeaderQuota,
    ]
  );
  const getDisplayCodexResetEvidenceQuota = useCallback(
    (row: AccountRow): CodexQuotaState | undefined =>
      mergeCodexResetCreditsFromQuotaSnapshots(
        getDisplayCodexQuota(row.raw),
        quotaSnapshotWindowsByRowKey.get(row.selectionKey) ?? []
      ),
    [getDisplayCodexQuota, quotaSnapshotWindowsByRowKey]
  );
  const getQuotaSnapshotObservation = useCallback(
    (row: AccountRow): AccountQuotaSnapshotObservationInput | undefined => {
      let fetchedAtMs: number | undefined;
      let inventoryMode: AccountQuotaSnapshotObservationInput['inventory_mode'] = 'complete';
      switch (row.provider) {
        case CODEX_CONFIG.type: {
          const state = getActiveCodexQuota(row.raw);
          if (
            state?.status === 'success' &&
            (getCodexQuotaEvidenceAtMs(state) ?? 0) >
              getCredentialEvidenceBoundary(row.raw).localAtMs &&
            (state.quotaInventoryObserved === true || state.windows.length > 0)
          ) {
            fetchedAtMs = state.fetchedAtMs;
            if (state.quotaInventoryObserved !== true) inventoryMode = 'partial';
            break;
          }
          break;
        }
        case CLAUDE_CONFIG.type: {
          const state = getCredentialScopedQuotaState(baseQuotaStores.claudeQuota, row.raw);
          if (
            state?.status === 'success' &&
            (state.quotaInventoryObserved === true || state.windows.length > 0)
          ) {
            fetchedAtMs = state.fetchedAtMs;
            if (state.quotaInventoryObserved !== true) inventoryMode = 'partial';
          }
          break;
        }
        case ANTIGRAVITY_CONFIG.type: {
          const state = getCredentialScopedQuotaState(baseQuotaStores.antigravityQuota, row.raw);
          if (
            state?.status === 'success' &&
            (state.quotaInventoryObserved === true || state.groups.length > 0)
          ) {
            fetchedAtMs = state.fetchedAtMs;
            if (state.quotaInventoryObserved !== true) inventoryMode = 'partial';
          }
          break;
        }
        case KIMI_CONFIG.type: {
          const state = getCredentialScopedQuotaState(baseQuotaStores.kimiQuota, row.raw);
          if (
            state?.status === 'success' &&
            (state.quotaInventoryObserved === true || state.rows.length > 0)
          ) {
            fetchedAtMs = state.fetchedAtMs;
            if (state.quotaInventoryObserved !== true) inventoryMode = 'partial';
          }
          break;
        }
        case XAI_CONFIG.type: {
          const state = getCredentialScopedQuotaState(baseQuotaStores.xaiQuota, row.raw);
          if (state?.status === 'success' && state.billing && !state.billing.officialApiHealth) {
            fetchedAtMs = state.fetchedAtMs;
            if (state.billing.partial !== false) inventoryMode = 'partial';
          }
          break;
        }
        default:
          return undefined;
      }
      if (typeof fetchedAtMs !== 'number' || !Number.isFinite(fetchedAtMs) || fetchedAtMs <= 0) {
        return undefined;
      }
      return {
        source: 'api_query',
        source_observation_id: `accounts-provider-query:${Math.trunc(fetchedAtMs)}`,
        observed_at_ms: Math.trunc(fetchedAtMs),
        inventory_scope_key:
          row.provider === CODEX_CONFIG.type
            ? 'codex:rate-limits'
            : `${row.provider}:quota-windows`,
        inventory_mode: inventoryMode,
      };
    },
    [baseQuotaStores, getActiveCodexQuota, getCredentialEvidenceBoundary]
  );
  const getCodexHeaderQuotaSnapshotObservation = useCallback(
    (row: AccountRow): AccountQuotaSnapshotObservationInput | undefined => {
      if (row.provider !== CODEX_CONFIG.type) return undefined;
      const activeQuota = getActiveCodexQuota(row.raw);
      if (
        activeQuota?.status === 'error' &&
        activeQuota.errorStatus === 401 &&
        (getCodexQuotaEvidenceAtMs(activeQuota) ?? 0) >
          getCredentialEvidenceBoundary(row.raw).localAtMs
      ) {
        return undefined;
      }
      const headerSnapshot = getDisplayCodexHeaderSnapshot(row.raw);
      if (
        !headerSnapshot ||
        !hasUsageHeaderQuotaSignal(headerSnapshot) ||
        !Number.isFinite(headerSnapshot.timestamp_ms) ||
        headerSnapshot.timestamp_ms <= 0
      ) {
        return undefined;
      }
      if (
        activeQuota?.status === 'success' &&
        activeQuota.quotaInventoryObserved === true &&
        typeof activeQuota.fetchedAtMs === 'number' &&
        Number.isFinite(activeQuota.fetchedAtMs) &&
        headerSnapshot.timestamp_ms <= activeQuota.fetchedAtMs
      ) {
        return undefined;
      }
      return {
        source: 'response_header',
        source_observation_id: headerSnapshot.event_hash,
        observed_at_ms: Math.trunc(headerSnapshot.timestamp_ms),
        inventory_scope_key: 'codex:rate-limits',
        inventory_mode: 'partial',
      };
    },
    [getActiveCodexQuota, getCredentialEvidenceBoundary, getDisplayCodexHeaderSnapshot]
  );
  const freshAccountInspectionBySelectionKey = useMemo(() => {
    const fresh = new Map(accountInspectionBySelectionKey);
    files.forEach((file) => {
      const selectionKey = getAuthFileSelectionKey(file);
      const inspection = fresh.get(selectionKey);
      if (!inspection) return;
      if (
        inspection.createdAtMs <=
        (credentialStatusBoundaries.get(selectionKey)?.inspectionAtMs ?? 0)
      ) {
        fresh.delete(selectionKey);
        return;
      }
      const providerQuota = getActiveCodexQuota(file);
      const headerQuota = getFreshCodexHeaderQuota(file);
      const cutoffs = getAccountCredentialEvidenceCutoffs({
        providerQuota,
        headerQuota,
        credentialRefreshAtMs: readAuthFileCredentialRefreshAtMs(file) ?? 0,
      });
      const provider401AtMs =
        providerQuota?.status === 'error' && providerQuota.errorStatus === 401
          ? getCodexQuotaEvidenceAtMs(providerQuota)
          : null;
      if (Math.max(cutoffs.authenticationAtMs, provider401AtMs ?? 0) >= inspection.createdAtMs) {
        fresh.delete(selectionKey);
      }
    });
    return fresh;
  }, [
    accountInspectionBySelectionKey,
    credentialStatusBoundaries,
    files,
    getActiveCodexQuota,
    getFreshCodexHeaderQuota,
  ]);
  const accountQuotaOverrides = useMemo(() => {
    const codexQuotaBySelectionKey = new Map<string, CodexQuotaState>();
    const codexHeaderSnapshotBySelectionKey = new Map<string, UsageHeaderSnapshot>();
    files.forEach((file) => {
      const selectionKey = getAuthFileSelectionKey(file);
      const headerSnapshot = getDisplayCodexHeaderSnapshot(file);
      if (headerSnapshot) {
        codexHeaderSnapshotBySelectionKey.set(selectionKey, headerSnapshot);
      }
      const quota = getDisplayCodexQuota(file);
      if (quota) {
        codexQuotaBySelectionKey.set(selectionKey, quota);
      }
    });
    return { codexQuotaBySelectionKey, codexHeaderSnapshotBySelectionKey };
  }, [files, getDisplayCodexHeaderSnapshot, getDisplayCodexQuota]);

  const rows = useMemo(
    () =>
      buildAccountRows(
        files,
        baseQuotaStores,
        effectiveInspectionResults,
        accountQuotaOverrides,
        freshAccountInspectionBySelectionKey,
        credentialEvidenceBoundariesBySelectionKey,
        credentialStatusBoundaries
      ),
    [
      accountQuotaOverrides,
      baseQuotaStores,
      files,
      credentialEvidenceBoundariesBySelectionKey,
      credentialStatusBoundaries,
      effectiveInspectionResults,
      freshAccountInspectionBySelectionKey,
    ]
  );
  const codexStatusBySelectionKey = useMemo(() => {
    const statusMap = new Map<string, ReturnType<typeof getAuthFileCodexStatus>>();
    rows.forEach((row) => {
      if (row.provider !== CODEX_CONFIG.type && row.provider !== XAI_CONFIG.type) return;
      const rawActiveQuota =
        row.provider === CODEX_CONFIG.type ? getActiveCodexQuota(row.raw) : undefined;
      const effectiveHeaderSnapshot = getDisplayCodexHeaderSnapshot(row.raw);
      const newerSuccessfulRequestAtMs = getLatestPositiveRequestAtMs(row.raw);
      const requestHealthEvidence = resolveAccountRequestHealthEvidence(
        requestEvidenceBySelectionKey.get(row.selectionKey)
      );
      const sources = getFreshAuthFileCodexStatusSources(
        row.raw,
        rawActiveQuota,
        isAccountInspectionStatusEvidenceCurrent(row, requestHealthEvidence)
          ? toAuthFileCodexInspectionSnapshot(row)
          : undefined,
        effectiveHeaderSnapshot,
        newerSuccessfulRequestAtMs,
        headerSnapshotGeneratedAtMs
      );
      const statusQuota =
        row.provider === CODEX_CONFIG.type ? getDisplayCodexQuota(row.raw) : undefined;
      statusMap.set(
        row.selectionKey,
        getAuthFileCodexStatus(
          row.raw,
          statusQuota,
          sources.inspection,
          sources.headerSnapshot,
          headerSnapshotGeneratedAtMs,
          {
            ignoreRawStatusCode: row.rawCredentialStatusSuperseded,
            effectiveDisabled: row.disabled,
          }
        )
      );
    });
    return statusMap;
  }, [
    getActiveCodexQuota,
    getDisplayCodexQuota,
    getDisplayCodexHeaderSnapshot,
    getLatestPositiveRequestAtMs,
    headerSnapshotGeneratedAtMs,
    requestEvidenceBySelectionKey,
    rows,
  ]);
  const actionCandidatesByRowKey = useMemo(() => {
    const itemsByRowKey = buildAccountOperationalItemsByRowKey(
      rows,
      accountActionCandidates.filter((candidate) => candidate.status === 'pending')
    );
    rows.forEach((row) => {
      const evidenceBoundary = getCredentialEvidenceBoundary(row.raw);
      const statusBoundary = credentialStatusBoundaries.get(row.selectionKey);
      const cutoffs = getAccountCredentialEvidenceCutoffs({
        providerQuota:
          row.provider === CODEX_CONFIG.type ? getActiveCodexQuota(row.raw) : undefined,
        headerQuota:
          row.provider === CODEX_CONFIG.type ? getFreshCodexHeaderQuota(row.raw) : undefined,
        inspection: row.inspection,
        authenticationBoundaryAtMs: Math.max(
          evidenceBoundary.authenticationAtMs,
          statusBoundary?.authenticationAtMs ?? 0
        ),
        credentialRefreshAtMs: readAuthFileCredentialRefreshAtMs(row.raw) ?? 0,
      });
      const effectiveInspectionAction = getEffectiveAccountInspectionAction(row.inspection);
      const executedInspectionAction = row.inspection?.executedAction?.trim().toLowerCase() ?? '';
      const inspectionAction = executedInspectionAction || effectiveInspectionAction;
      const authenticationInspectionActionAtMs =
        row.inspection && (inspectionAction === 'reauth' || inspectionAction === 'delete')
          ? row.inspection.createdAtMs
          : 0;
      const quotaInspectionActionAtMs =
        row.inspection && (inspectionAction === 'disable' || inspectionAction === 'enable')
          ? row.inspection.createdAtMs
          : 0;
      itemsByRowKey.set(
        row.selectionKey,
        (itemsByRowKey.get(row.selectionKey) ?? []).filter((candidate) => {
          const candidateUsesFileNameFallback =
            getAccountOperationalItemIdentityKey(candidate) ===
            getAuthFileCodexInspectionKeyForIdentity({ fileName: candidate.authFileName });
          const fallbackActionAtMs = candidateUsesFileNameFallback
            ? evidenceBoundary.fallbackActionAtMs
            : 0;
          if (
            evidenceBoundary.actionBaselinePending === true ||
            statusBoundary?.actionBaselinePending === true ||
            (candidateUsesFileNameFallback &&
              (evidenceBoundary.fallbackActionBaselinePending === true ||
                statusBoundary?.fallbackActionBaselinePending === true))
          ) {
            return false;
          }
          const actionType = candidate.actionType.trim().toLowerCase();
          const isCredentialStateAction =
            actionType === 'reauth' ||
            actionType === 'delete' ||
            actionType === 'disable' ||
            actionType === 'enable';
          const authenticationAction = actionType === 'reauth' || actionType === 'delete';
          const quotaAction = actionType === 'disable' || actionType === 'enable';
          if (
            (authenticationAction &&
              statusBoundary?.authenticationActionBaselinePending === true) ||
            (quotaAction && statusBoundary?.quotaActionBaselinePending === true)
          ) {
            return false;
          }
          const statusActionAtMs = authenticationAction
            ? (statusBoundary?.authenticationActionAtMs ?? 0)
            : quotaAction
              ? (statusBoundary?.quotaActionAtMs ?? 0)
              : 0;
          let evidenceCutoffAtMs = 0;
          if (actionType === 'reauth' || actionType === 'delete') {
            evidenceCutoffAtMs = Math.max(
              cutoffs.authenticationAtMs,
              authenticationInspectionActionAtMs
            );
          } else if (actionType === 'disable' || actionType === 'enable') {
            evidenceCutoffAtMs = Math.max(cutoffs.healthyQuotaAtMs, quotaInspectionActionAtMs);
          }
          const cutoffAtMs = Math.max(
            evidenceCutoffAtMs,
            evidenceBoundary.actionAtMs,
            fallbackActionAtMs,
            isCredentialStateAction ? (statusBoundary?.actionAtMs ?? 0) : 0,
            statusActionAtMs
          );
          return !isEvidenceOlderThan(
            Math.max(candidate.updatedAtMs, candidate.lastSeenAtMs, candidate.createdAtMs),
            cutoffAtMs
          );
        })
      );
    });
    return itemsByRowKey;
  }, [
    accountActionCandidates,
    credentialStatusBoundaries,
    getActiveCodexQuota,
    getCredentialEvidenceBoundary,
    getFreshCodexHeaderQuota,
    rows,
  ]);
  const quotaCooldownsByRowKey = useMemo(() => {
    const itemsByRowKey = buildAccountOperationalItemsByRowKey(
      rows.filter((row) => row.provider === CODEX_CONFIG.type || row.provider === XAI_CONFIG.type),
      Array.from(quotaCooldowns.values())
    );
    rows.forEach((row) => {
      const evidenceBoundary = getCredentialEvidenceBoundary(row.raw);
      const statusBoundary = credentialStatusBoundaries.get(row.selectionKey);
      const cutoffs = getAccountCredentialEvidenceCutoffs({
        providerQuota:
          row.provider === CODEX_CONFIG.type ? getActiveCodexQuota(row.raw) : undefined,
        headerQuota:
          row.provider === CODEX_CONFIG.type ? getFreshCodexHeaderQuota(row.raw) : undefined,
        inspection: row.inspection,
        authenticationBoundaryAtMs: Math.max(
          evidenceBoundary.authenticationAtMs,
          statusBoundary?.authenticationAtMs ?? 0
        ),
        credentialRefreshAtMs: readAuthFileCredentialRefreshAtMs(row.raw) ?? 0,
      });
      itemsByRowKey.set(
        row.selectionKey,
        (itemsByRowKey.get(row.selectionKey) ?? []).filter((cooldown) => {
          const cooldownUsesFileNameFallback =
            getAccountOperationalItemIdentityKey(cooldown) ===
            getAuthFileCodexInspectionKeyForIdentity({ fileName: cooldown.authFileName });
          if (
            evidenceBoundary.cooldownBaselinePending === true ||
            statusBoundary?.cooldownBaselinePending === true ||
            (cooldownUsesFileNameFallback &&
              (evidenceBoundary.fallbackCooldownBaselinePending === true ||
                statusBoundary?.fallbackCooldownBaselinePending === true))
          ) {
            return false;
          }
          return !isEvidenceOlderThan(
            Math.max(cooldown.disabledAtMs ?? 0, cooldown.createdAtMs ?? 0),
            Math.max(
              cutoffs.healthyQuotaAtMs,
              evidenceBoundary.cooldownAtMs,
              cooldownUsesFileNameFallback ? evidenceBoundary.fallbackCooldownAtMs : 0,
              statusBoundary?.cooldownAtMs ?? 0
            )
          );
        })
      );
    });
    return itemsByRowKey;
  }, [
    credentialStatusBoundaries,
    getActiveCodexQuota,
    getCredentialEvidenceBoundary,
    getFreshCodexHeaderQuota,
    quotaCooldowns,
    rows,
  ]);
  const metrics = useMemo(
    () =>
      buildAccountMetrics(rows, {
        pendingActionsByRowKey: actionCandidatesByRowKey,
        quotaCooldownsByRowKey,
        requestEvidenceBySelectionKey,
      }),
    [actionCandidatesByRowKey, quotaCooldownsByRowKey, requestEvidenceBySelectionKey, rows]
  );
  const providerOptions = useMemo(() => getProviderOptions(rows), [rows]);
  const planOptions = useMemo(() => getPlanOptions(rows, t), [rows, t]);
  const planFilterValue = useMemo(
    () => getPlanOptionValue(rows, planFilter, t),
    [planFilter, rows, t]
  );
  const effectivePlanOptions = useMemo(() => {
    if (
      planFilterValue === 'all' ||
      planOptions.some((option) => option.value === planFilterValue)
    ) {
      return planOptions;
    }

    return [
      ...planOptions,
      {
        value: planFilterValue,
        label: getPlanOptionLabel(rows, planFilterValue, t),
      },
    ];
  }, [planFilterValue, planOptions, rows, t]);
  const recommendations = useMemo(
    () => buildAccountRecommendations(rows, requestEvidenceBySelectionKey),
    [requestEvidenceBySelectionKey, rows]
  );
  const recommendationBySelectionKey = useMemo(
    () => buildRecommendationBySelectionKey(recommendations),
    [recommendations]
  );
  const baseFilteredRows = useMemo(
    () =>
      filterAccountRows(rows, {
        provider: providerFilter,
        status: statusFilter,
        plan: planFilterValue,
        quotaBand: quotaBandFilter,
        search,
        codexStatusBySelectionKey,
        pendingActionsByRowKey: actionCandidatesByRowKey,
        quotaCooldownsByRowKey,
        requestEvidenceBySelectionKey,
      }),
    [
      actionCandidatesByRowKey,
      codexStatusBySelectionKey,
      planFilterValue,
      providerFilter,
      quotaCooldownsByRowKey,
      quotaBandFilter,
      requestEvidenceBySelectionKey,
      rows,
      search,
      statusFilter,
    ]
  );
  const filteredRows = useMemo(() => {
    const operationalRows = baseFilteredRows.filter((row) => {
      if (effectiveOperationalFilter === 'all') return true;
      if (effectiveOperationalFilter === 'reauth') {
        const recommendation = recommendationBySelectionKey.get(row.selectionKey);
        const codexStatus = codexStatusBySelectionKey.get(row.selectionKey);
        const requestEvidence = resolveAccountRequestHealthEvidence(
          requestEvidenceBySelectionKey.get(row.selectionKey)
        );
        const requestCredentialEvidence = isAccountRequestCredentialEvidenceCurrent(
          row,
          requestEvidence
        )
          ? getAccountRequestCredentialEvidence(requestEvidence)
          : null;
        const authenticationProblem = resolveAccountAuthenticationProblemEvidence(
          row,
          requestEvidence
        );
        return (
          recommendation?.action === 'reauth' ||
          authenticationProblem !== null ||
          (codexStatus?.needsReauth === true && requestCredentialEvidence?.direction !== 'positive')
        );
      }
      if (effectiveOperationalFilter === 'cooldown') {
        return (quotaCooldownsByRowKey.get(row.selectionKey)?.length ?? 0) > 0;
      }
      if (effectiveOperationalFilter === 'automation') {
        return (actionCandidatesByRowKey.get(row.selectionKey)?.length ?? 0) > 0;
      }
      return (
        recommendationBySelectionKey.get(row.selectionKey)?.reasonKey ===
        'accounts.recommend_reason_recovered'
      );
    });
    return sortAccountRows(operationalRows, accountSort, requestEvidenceBySelectionKey);
  }, [
    accountSort,
    actionCandidatesByRowKey,
    baseFilteredRows,
    codexStatusBySelectionKey,
    effectiveOperationalFilter,
    quotaCooldownsByRowKey,
    recommendationBySelectionKey,
    requestEvidenceBySelectionKey,
  ]);

  const totalPages = Math.max(1, Math.ceil(filteredRows.length / pageSize));
  const currentPage = Math.min(page, totalPages);
  const pageRows = useMemo(
    () => filteredRows.slice((currentPage - 1) * pageSize, currentPage * pageSize),
    [currentPage, filteredRows, pageSize]
  );
  const paginationStartItem = filteredRows.length === 0 ? 0 : (currentPage - 1) * pageSize + 1;
  const paginationEndItem = Math.min(filteredRows.length, currentPage * pageSize);
  const pageAuthFiles = useMemo(() => pageRows.map((row) => row.raw), [pageRows]);
  const filteredAuthFiles = useMemo(() => filteredRows.map((row) => row.raw), [filteredRows]);
  const selectablePageRows = useMemo(() => pageRows.filter((row) => !row.runtimeOnly), [pageRows]);
  const selectableFilteredRows = useMemo(
    () => filteredRows.filter((row) => !row.runtimeOnly),
    [filteredRows]
  );
  const selectedRows = useMemo(
    () => rows.filter((row) => selectedFiles.has(row.selectionKey)),
    [rows, selectedFiles]
  );
  const selectedTargetFiles = useMemo(
    () => selectedRows.filter((row) => !row.runtimeOnly).map((row) => row.raw),
    [selectedRows]
  );
  const selectedFileNames = useMemo(
    () => Array.from(new Set(selectedTargetFiles.map((file) => file.name))),
    [selectedTargetFiles]
  );
  const selectedHasPartialSharedAuthFile = useMemo(
    () => hasPartialSharedAuthFileSelection(files, selectedFiles),
    [files, selectedFiles]
  );
  const selectedCodexRows = useMemo(
    () => selectedRows.filter((row) => !row.runtimeOnly && row.provider === CODEX_CONFIG.type),
    [selectedRows]
  );
  const selectedRow = useMemo(
    () => rows.find((row) => row.selectionKey === selectedRowKey) ?? null,
    [rows, selectedRowKey]
  );
  const accountHistoryTargets = useMemo(() => buildAccountHistoryTargetEntries(rows), [rows]);
  const accountHistoryAutoContextKey = useMemo(
    () =>
      JSON.stringify({
        checking: featureAvailability.checking,
        connectionFingerprint,
        managerServiceBase: featureAvailability.managerServiceBase,
        managementKey,
        managerConnectionFingerprint,
        requestMonitoringAvailable: featureAvailability.requestMonitoringAvailable,
        targets: accountHistoryTargets.map((entry) => entry.target),
      }),
    [
      accountHistoryTargets,
      connectionFingerprint,
      featureAvailability.checking,
      featureAvailability.managerServiceBase,
      featureAvailability.requestMonitoringAvailable,
      managerConnectionFingerprint,
      managementKey,
    ]
  );
  const accountHistoryAutoLoadKey = `${accountHistoryAutoContextKey}\u0000${accountHistoryAutoRefreshRevision}`;
  const usageValuesAutoLoadKey = useMemo(
    () =>
      JSON.stringify({
        checking: featureAvailability.checking,
        connectionFingerprint,
        historyRevision: accountHistoryRefreshRevision,
        managerConnectionFingerprint,
        requestMonitoringAvailable: featureAvailability.requestMonitoringAvailable,
        selectedRowKey: selectedRow?.selectionKey ?? selectedRowKey,
      }),
    [
      accountHistoryRefreshRevision,
      connectionFingerprint,
      featureAvailability.checking,
      featureAvailability.requestMonitoringAvailable,
      managerConnectionFingerprint,
      selectedRow,
      selectedRowKey,
    ]
  );
  const detailEventsAutoLoadKey = useMemo(
    () =>
      JSON.stringify({
        checking: featureAvailability.checking,
        connectionFingerprint,
        managerConnectionFingerprint,
        requestMonitoringAvailable: featureAvailability.requestMonitoringAvailable,
        selectedRowKey: selectedRow?.selectionKey ?? selectedRowKey,
      }),
    [
      connectionFingerprint,
      featureAvailability.checking,
      featureAvailability.requestMonitoringAvailable,
      managerConnectionFingerprint,
      selectedRow,
      selectedRowKey,
    ]
  );
  const selectedSourceMemberCount = useMemo(() => {
    const fileName = selectedRow?.fileName.trim();
    if (!fileName) return 0;
    return files.filter((file) => String(file.name ?? '').trim() === fileName).length;
  }, [files, selectedRow?.fileName]);
  const disableControls = connectionStatus !== 'connected';
  const handleConfigurationSaved = useCallback(() => {
    if (!selectedRow) return;
    invalidateModels(selectedRow.raw);
    if (detailTab === 'models') {
      void refreshModels(selectedRow.raw);
    }
  }, [detailTab, invalidateModels, refreshModels, selectedRow]);
  const configurationEditor = useAuthFileConfigurationEditor({
    file: selectedRow && !selectedRow.runtimeOnly ? selectedRow.raw : null,
    enabled:
      activeView === 'accounts' &&
      (detailTab === 'config' || detailTab === 'models') &&
      Boolean(selectedRow),
    disableControls,
    sourceMemberCount: selectedSourceMemberCount,
    connectionKey: connectionFingerprint,
    requestScope: authFilesRequestScope,
    loadFiles: async () => {
      await loadFiles();
    },
    onSaved: handleConfigurationSaved,
  });
  const configurationDirty = configurationEditor.dirty;
  const configurationReload = configurationEditor.reload;
  const configurationSaving = configurationEditor.state?.saving === true;
  const handleAccountModelsRefresh = useCallback(async () => {
    if (!selectedRow) return;
    await Promise.all([
      refreshModels(selectedRow.raw),
      loadOauthExcluded(),
      loadOauthModelAlias(),
      ...(!configurationDirty && !configurationSaving && !selectedRow.runtimeOnly
        ? [configurationReload()]
        : []),
    ]);
  }, [
    configurationDirty,
    configurationReload,
    configurationSaving,
    loadOauthExcluded,
    loadOauthModelAlias,
    refreshModels,
    selectedRow,
  ]);
  const shouldBlockConfigurationNavigation = useCallback<BlockerFunction>(
    ({ currentLocation, nextLocation }) => {
      if (!configurationEditor.dirty && !configurationSaving) return false;
      if (currentLocation.pathname !== nextLocation.pathname) return true;

      const current = readAccountsWorkspaceUrlState(
        currentLocation.search,
        DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE
      );
      const next = readAccountsWorkspaceUrlState(
        nextLocation.search,
        DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE
      );
      return !(
        current.view === 'accounts' &&
        next.view === 'accounts' &&
        Boolean(current.account) &&
        current.account === next.account &&
        isConfigurationDetailTab(current.detailTab) &&
        isConfigurationDetailTab(next.detailTab)
      );
    },
    [configurationEditor.dirty, configurationSaving]
  );
  const handleRouterNavigationConfirm = useCallback((): boolean => {
    if (configurationSaving) {
      showNotification(t('accounts.config_save_in_progress'), 'info');
      return false;
    }
    configurationEditor.reset();
    return true;
  }, [configurationEditor, configurationSaving, showNotification, t]);
  const { allowNextNavigation, allowNavigationTo } = useUnsavedChangesGuard({
    enabled: activeView === 'accounts' && (detailTab === 'config' || detailTab === 'models'),
    shouldBlock: shouldBlockConfigurationNavigation,
    onConfirmNavigation: handleRouterNavigationConfirm,
    dialog: {
      title: t('common.unsaved_changes_title'),
      message: t('common.unsaved_changes_message'),
      confirmText: t('common.leave'),
      cancelText: t('common.stay'),
      variant: 'danger',
    },
  });
  const discardConfigurationRequestRef = useRef<Promise<boolean> | null>(null);
  const confirmConfigurationDiscard = useCallback(
    (resetDraftOnConfirm = true): Promise<boolean> => {
      if (configurationEditor.state?.saving) {
        showNotification(t('accounts.config_save_in_progress'), 'info');
        return Promise.resolve(false);
      }
      if (!configurationEditor.dirty) return Promise.resolve(true);
      if (discardConfigurationRequestRef.current) {
        return Promise.resolve(false);
      }

      const request = new Promise<boolean>((resolve) => {
        showConfirmation({
          title: t('common.unsaved_changes_title'),
          message: t('common.unsaved_changes_message'),
          confirmText: t('common.leave'),
          cancelText: t('common.stay'),
          variant: 'danger',
          onConfirm: () => {
            if (resetDraftOnConfirm) configurationEditor.reset();
            resolve(true);
          },
          onCancel: () => resolve(false),
        });
      });
      discardConfigurationRequestRef.current = request;
      void request.finally(() => {
        if (discardConfigurationRequestRef.current === request) {
          discardConfigurationRequestRef.current = null;
        }
      });
      return request;
    },
    [configurationEditor, showConfirmation, showNotification, t]
  );
  const handleAccountDelete = useCallback(
    async (item: AuthFileItem) => {
      const deletesOpenCredential = getAuthFileSelectionKey(item) === selectedRowKey;
      if (deletesOpenCredential && !(await confirmConfigurationDiscard(false))) return;
      handleDelete(item);
    },
    [confirmConfigurationDiscard, handleDelete, selectedRowKey]
  );
  const hasSelectedAccountDetail = activeView === 'accounts' && Boolean(selectedRowKey);
  const needsInspectionSummary = canLoadInspectionSummary;
  const needsQuotaCooldowns =
    managerStorageAvailable &&
    activeView === 'accounts' &&
    (effectiveOperationalFilter === 'cooldown' ||
      (hasSelectedAccountDetail && (detailTab === 'overview' || detailTab === 'quota')));
  const needsActionCandidates =
    managerStorageAvailable &&
    activeView === 'accounts' &&
    (effectiveOperationalFilter === 'automation' ||
      (hasSelectedAccountDetail && (detailTab === 'overview' || detailTab === 'diagnostics')));
  const needsHeaderSnapshots =
    canLoadHeaderSnapshots && headerSnapshotLoadedKey !== headerSnapshotLoadKey;

  useEffect(() => {
    if (
      !needsQuotaCooldowns ||
      credentialMutationMarkerSynchronizationsRef.current.has(credentialEvidenceScopeKey)
    ) {
      return;
    }
    void loadQuotaCooldowns();
  }, [credentialEvidenceScopeKey, loadQuotaCooldowns, needsQuotaCooldowns]);

  useEffect(() => {
    if (
      !needsActionCandidates ||
      credentialMutationMarkerSynchronizationsRef.current.has(credentialEvidenceScopeKey)
    ) {
      return;
    }
    void loadAccountActionCandidates();
  }, [credentialEvidenceScopeKey, loadAccountActionCandidates, needsActionCandidates]);

  useEffect(() => {
    if (!needsHeaderSnapshots) return;
    void loadHeaderSnapshots();
  }, [loadHeaderSnapshots, needsHeaderSnapshots]);

  useEffect(() => {
    if (!needsInspectionSummary) return;
    void loadInspectionSummaryAndSynchronize();
  }, [loadInspectionSummaryAndSynchronize, needsInspectionSummary]);

  useEffect(() => {
    if (activeView !== 'accounts' || detailTab !== 'models' || !selectedRow) {
      modelsLoadKeyRef.current = '';
      return;
    }

    const loadKey = `${connectionFingerprint ?? ''}\u0000${selectedRow.selectionKey}`;
    if (modelsLoadKeyRef.current === loadKey) return;
    modelsLoadKeyRef.current = loadKey;
    void showModels(selectedRow.raw);
  }, [activeView, connectionFingerprint, detailTab, selectedRow, showModels]);

  useEffect(() => {
    if (activeView !== 'accounts' || detailTab !== 'models' || !selectedRow) {
      modelRulesLoadKeyRef.current = '';
      return;
    }

    const loadKey = `${connectionFingerprint}\u0000models`;
    if (modelRulesLoadKeyRef.current === loadKey) return;
    modelRulesLoadKeyRef.current = loadKey;
    void Promise.all([loadOauthExcluded(), loadOauthModelAlias()]);
  }, [
    activeView,
    connectionFingerprint,
    detailTab,
    loadOauthExcluded,
    loadOauthModelAlias,
    selectedRow,
  ]);

  const handleAccountCardClick = useCallback(
    (row: AccountRow) => {
      if (isSelectionMode) {
        if (!row.runtimeOnly) {
          toggleSelect(row.selectionKey);
        }
      }
    },
    [isSelectionMode, toggleSelect]
  );
  const cancelSelectionMode = useCallback(() => {
    deselectAll();
    setIsSelectionMode(false);
  }, [deselectAll]);
  const getDisplayText = useCallback(
    (value: string) => (accountDisplayMode === 'full' ? value : maskQuotaAccountText(value)),
    [accountDisplayMode]
  );
  const getDisplayAccount = useCallback(
    (row: AccountRow) => getDisplayText(row.accountLabel),
    [getDisplayText]
  );
  const getDisplayFileName = useCallback(
    (fileName: string) => getDisplayText(fileName),
    [getDisplayText]
  );
  const translateQuotaWindowLabel = useCallback(
    (
      label: string | undefined,
      labelKey?: string,
      labelParams?: Record<string, string | number>
    ) => {
      if (!labelKey) return label || t('accounts.col_quota');
      return t(labelKey, {
        defaultValue: label || labelKey,
        ...labelParams,
      });
    },
    [t]
  );
  const buildQuotaDisplayWindows = useCallback(
    (row: AccountRow): AccountQuotaDisplayWindow[] => {
      return buildAccountQuotaDisplayWindows(row, {
        stores: baseQuotaStores,
        getDisplayCodexQuota,
        translateQuotaWindowLabel,
        t,
      });
    },
    [baseQuotaStores, getDisplayCodexQuota, t, translateQuotaWindowLabel]
  );
  const buildCodexSnapshotDefinitions = useCallback(
    (row: AccountRow, quota: CodexQuotaState | undefined): AccountQuotaWindowDefinition[] =>
      buildAccountQuotaWindowDefinitions(
        buildAccountQuotaDisplayWindows(row, {
          stores: baseQuotaStores,
          getDisplayCodexQuota: () => quota,
          translateQuotaWindowLabel,
          t,
        })
      ).filter((definition) => definition.provider === CODEX_CONFIG.type),
    [baseQuotaStores, t, translateQuotaWindowLabel]
  );
  const quotaDisplayWindowsByRowKey = useMemo(() => {
    const result = new Map<string, AccountQuotaDisplayWindow[]>();
    pageRows.forEach((row) => {
      result.set(row.selectionKey, buildQuotaDisplayWindows(row));
    });
    if (selectedRow && !result.has(selectedRow.selectionKey)) {
      result.set(selectedRow.selectionKey, buildQuotaDisplayWindows(selectedRow));
    }
    return result;
  }, [buildQuotaDisplayWindows, pageRows, selectedRow]);
  const quotaWindowDefinitionsByRowKey = useMemo(() => {
    const result = new Map<string, AccountQuotaWindowDefinition[]>();
    quotaDisplayWindowsByRowKey.forEach((windows, rowKey) => {
      result.set(rowKey, buildAccountQuotaWindowDefinitions(windows));
    });
    return result;
  }, [quotaDisplayWindowsByRowKey]);
  const effectiveQuotaWindowDefinitionsByRowKey = useMemo(() => {
    const result = new Map<string, AccountQuotaWindowDefinition[]>();
    quotaWindowDefinitionsByRowKey.forEach((definitions, rowKey) => {
      const provider = selectedRow?.selectionKey === rowKey ? selectedRow.provider : undefined;
      result.set(
        rowKey,
        mergeAccountQuotaSnapshotWindows(
          definitions,
          quotaSnapshotWindowsByRowKey.get(rowKey) ?? [],
          {
            provider,
            getLabel: (snapshot) => {
              const kind = snapshot.window_kind;
              if (kind === 'rolling_24h') {
                return t('accounts.detail_snapshot_window_rolling_24h');
              }
              if (kind === 'five_hour') return t('accounts.detail_snapshot_window_five_hour');
              if (kind === 'daily') return t('accounts.detail_snapshot_window_daily');
              if (kind === 'weekly') return t('accounts.detail_snapshot_window_weekly');
              if (kind === 'monthly') return t('accounts.detail_snapshot_window_monthly');
              return snapshot.provider_window_id;
            },
          }
        )
      );
    });
    return result;
  }, [quotaSnapshotWindowsByRowKey, quotaWindowDefinitionsByRowKey, selectedRow, t]);
  const accountWindowUsageTargets = useMemo(() => {
    if (!selectedRow) return [];
    const windowsByRowKey = new Map<string, AccountQuotaWindowDefinition[]>();
    windowsByRowKey.set(
      selectedRow.selectionKey,
      effectiveQuotaWindowDefinitionsByRowKey.get(selectedRow.selectionKey) ??
        buildAccountQuotaWindowDefinitions(buildQuotaDisplayWindows(selectedRow))
    );
    const asOfMs =
      accountWindowUsageQueryContext?.rowKey === selectedRow.selectionKey
        ? accountWindowUsageQueryContext.asOfMs
        : undefined;
    return buildAccountWindowUsageTargetEntries([selectedRow], windowsByRowKey, asOfMs);
  }, [
    accountWindowUsageQueryContext,
    buildQuotaDisplayWindows,
    effectiveQuotaWindowDefinitionsByRowKey,
    selectedRow,
  ]);
  const matchingAccountWindowUsageByKey = useMemo(
    () =>
      filterAccountWindowUsageByTargetRanges(accountWindowUsageTargets, accountWindowUsageByKey),
    [accountWindowUsageByKey, accountWindowUsageTargets]
  );
  const selectedHeaderSnapshotRevision = useMemo(() => {
    if (selectedRow?.provider !== CODEX_CONFIG.type) return '';
    const snapshot = getEffectiveCodexHeaderSnapshot(selectedRow.raw);
    return snapshot ? `${snapshot.event_hash}\u0000${snapshot.timestamp_ms}` : '';
  }, [getEffectiveCodexHeaderSnapshot, selectedRow]);
  const accountWindowUsageAutoLoadKey = useMemo(
    () =>
      JSON.stringify({
        checking: featureAvailability.checking,
        managerConnectionFingerprint,
        requestMonitoringAvailable: featureAvailability.requestMonitoringAvailable,
        selectedRowKey: selectedRow?.selectionKey ?? selectedRowKey,
        selectedHeaderSnapshotRevision,
        historyRevision: accountHistoryRefreshRevision,
        quotaRevision: accountQuotaRefreshRevision,
      }),
    [
      accountHistoryRefreshRevision,
      accountQuotaRefreshRevision,
      featureAvailability.checking,
      featureAvailability.requestMonitoringAvailable,
      managerConnectionFingerprint,
      selectedRow,
      selectedHeaderSnapshotRevision,
      selectedRowKey,
    ]
  );
  const accountDisplayHint = t(
    accountDisplayMode === 'masked'
      ? 'accounts.show_full_credentials_hint'
      : 'accounts.show_masked_credentials_hint'
  );
  const AccountDisplayIcon = accountDisplayMode === 'masked' ? IconEyeOff : IconEye;
  const oauthPreviewProviders = useMemo(
    () =>
      getOAuthRulePreviewProviders({
        providers: providerOptions,
        excluded: oauthState.excluded,
        aliases: oauthState.modelAlias,
      }),
    [oauthState.excluded, oauthState.modelAlias, providerOptions]
  );
  const oauthPreviewProviderOptions = useMemo(
    () => [
      { value: '', label: t('accounts.oauth_preview_provider_all') },
      ...oauthPreviewProviders.map((provider) => ({
        value: provider,
        label: getProviderLabel(provider, t),
      })),
    ],
    [oauthPreviewProviders, t]
  );
  const oauthPreviewRows = useMemo(
    () =>
      buildOAuthRulePreviewRows({
        providers: providerOptions,
        excluded: oauthState.excluded,
        aliases: oauthState.modelAlias,
        inputModel: oauthPreviewModel,
      }),
    [oauthPreviewModel, oauthState.excluded, oauthState.modelAlias, providerOptions]
  );
  const { affectedRows: oauthAffectedPreviewRows, directRows: oauthDirectPreviewRows } = useMemo(
    () => partitionOAuthRulePreviewRows(oauthPreviewRows, oauthPreviewProvider),
    [oauthPreviewProvider, oauthPreviewRows]
  );

  useEffect(() => {
    setPage(1);
  }, [
    operationalFilter,
    pageSize,
    planFilter,
    providerFilter,
    quotaBandFilter,
    search,
    statusFilter,
  ]);

  useEffect(() => {
    writeAccountsWorkspaceUiState({
      search,
      providerFilter,
      statusFilter,
      planFilter,
      quotaBandFilter,
      operationalFilter,
      accountSort,
      pageSize,
      accountDisplayMode,
    });
  }, [
    accountDisplayMode,
    accountSort,
    operationalFilter,
    pageSize,
    planFilter,
    providerFilter,
    quotaBandFilter,
    search,
    statusFilter,
  ]);

  const workspaceUrlState = useMemo(
    () => ({
      search,
      providerFilter,
      statusFilter,
      planFilter,
      quotaBandFilter,
      operationalFilter,
      accountSort,
      pageSize,
      accountDisplayMode,
      view: activeView,
      healthMode,
      account: selectedRowKey,
      detailTab,
      editor:
        oauthExcludedEditorProvider !== null
          ? ('excluded' as const)
          : oauthModelAliasEditorProvider !== null
            ? ('alias' as const)
            : null,
      editorProvider: oauthExcludedEditorProvider ?? oauthModelAliasEditorProvider ?? '',
    }),
    [
      accountDisplayMode,
      accountSort,
      activeView,
      detailTab,
      healthMode,
      oauthExcludedEditorProvider,
      oauthModelAliasEditorProvider,
      operationalFilter,
      pageSize,
      planFilter,
      providerFilter,
      quotaBandFilter,
      search,
      selectedRowKey,
      statusFilter,
    ]
  );

  const openAccountDetail = useCallback(
    async (row: AccountRow, tab: DetailTab = 'overview') => {
      const preservesConfigurationDraft =
        row.selectionKey === selectedRowKey &&
        (detailTab === 'config' || detailTab === 'models') &&
        (tab === 'config' || tab === 'models');
      const hadDirtyConfiguration = configurationEditor.dirty;
      if (!preservesConfigurationDraft && !(await confirmConfigurationDiscard())) return;
      if (
        hadDirtyConfiguration &&
        (!preservesConfigurationDraft || row.selectionKey !== selectedRowKey || tab !== detailTab)
      ) {
        allowNextNavigation();
      }

      const searchValue = writeAccountsWorkspaceUrlSearch(
        location.search,
        {
          ...workspaceUrlState,
          view: 'accounts',
          account: row.selectionKey,
          detailTab: tab,
        },
        DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE
      );
      setSelectedRowKey(row.selectionKey);
      setDetailTab(tab);
      if (searchValue !== location.search) {
        lastWorkspaceNavigationRef.current = searchValue;
        navigate({ pathname: location.pathname, search: searchValue }, { replace: true });
      }
    },
    [
      allowNextNavigation,
      configurationEditor.dirty,
      confirmConfigurationDiscard,
      detailTab,
      location.pathname,
      location.search,
      navigate,
      selectedRowKey,
      workspaceUrlState,
    ]
  );

  const closeAccountDetail = useCallback(() => {
    setSelectedRowKey(null);
    setDetailTab('overview');
    const searchValue = writeAccountsWorkspaceUrlSearch(
      location.search,
      { ...workspaceUrlState, account: null, detailTab: 'overview' },
      DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE
    );
    lastWorkspaceNavigationRef.current = searchValue;
    allowNextNavigation();
    navigate({ pathname: location.pathname, search: searchValue }, { replace: true });
  }, [allowNextNavigation, location.pathname, location.search, navigate, workspaceUrlState]);

  const applyWorkspaceUrlState = useCallback(
    (searchValue: string, fallback: AccountsWorkspaceUiState) => {
      const next = readAccountsWorkspaceUrlState(searchValue, fallback);
      const requestedView = new URLSearchParams(searchValue).get('view');
      syncingWorkspaceLocationRef.current = !requestedView || requestedView === next.view;
      lastWorkspaceNavigationRef.current = null;
      setActiveView(next.view);
      setHealthMode(next.healthMode);
      setSearch(next.search);
      setProviderFilter(next.providerFilter);
      setStatusFilter(next.statusFilter);
      setPlanFilter(next.planFilter);
      setQuotaBandFilter(next.quotaBandFilter);
      setOperationalFilter(next.operationalFilter);
      setAccountSort(next.accountSort);
      setPageSize(next.pageSize);
      setAccountDisplayMode(next.accountDisplayMode);
      setSelectedRowKey(next.account);
      setDetailTab(next.detailTab);
      setOauthExcludedEditorProvider(next.editor === 'excluded' ? next.editorProvider : null);
      setOauthModelAliasEditorProvider(next.editor === 'alias' ? next.editorProvider : null);
    },
    []
  );

  useEffect(() => {
    const fallback = hasProcessedInitialWorkspaceLocationRef.current
      ? DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE
      : initialWorkspaceUiState.current;
    hasProcessedInitialWorkspaceLocationRef.current = true;
    applyWorkspaceUrlState(location.search, fallback);
  }, [applyWorkspaceUrlState, location.search]);

  useEffect(() => {
    if (typeof window === 'undefined') return;
    currentHashHistoryStateRef.current = window.history?.state;
  }, [location.hash, location.pathname, location.search]);

  useEffect(() => {
    if (syncingWorkspaceLocationRef.current) {
      syncingWorkspaceLocationRef.current = false;
      return;
    }
    const nextSearch = writeAccountsWorkspaceUrlSearch(
      location.search,
      workspaceUrlState,
      DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE
    );
    if (nextSearch === location.search) {
      lastWorkspaceNavigationRef.current = null;
      return;
    }
    if (lastWorkspaceNavigationRef.current === nextSearch) return;
    lastWorkspaceNavigationRef.current = nextSearch;
    navigate({ pathname: location.pathname, search: nextSearch }, { replace: true });
  }, [location.pathname, location.search, navigate, workspaceUrlState]);

  useEffect(() => {
    if (typeof window === 'undefined') return undefined;
    const syncWorkspaceFromHash = () => {
      if (configurationEditor.dirty || configurationEditor.state?.saving) return;
      applyWorkspaceUrlState(
        readAccountsSearchFromHash(window.location.hash),
        DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE
      );
    };
    window.addEventListener('hashchange', syncWorkspaceFromHash);
    return () => window.removeEventListener('hashchange', syncWorkspaceFromHash);
  }, [applyWorkspaceUrlState, configurationEditor.dirty, configurationEditor.state?.saving]);

  useEffect(() => {
    if (
      typeof window === 'undefined' ||
      !window.history ||
      typeof window.history.replaceState !== 'function'
    ) {
      return undefined;
    }

    const interceptExternalHashNavigation = (event: Event) => {
      const historyState = window.history.state as { idx?: unknown } | null;
      if (typeof historyState?.idx === 'number') return;

      const targetHash = window.location.hash;
      const currentHash = `#${location.pathname}${location.search}${location.hash || ''}`;
      if (!targetHash || targetHash === currentHash) return;

      event.stopImmediatePropagation();
      window.history.replaceState(currentHashHistoryStateRef.current, '', currentHash);

      const target = targetHash.startsWith('#') ? targetHash.slice(1) || '/' : targetHash;
      if (pendingExternalHashNavigationRef.current) return;
      pendingExternalHashNavigationRef.current = target;
      const finish = () => {
        if (pendingExternalHashNavigationRef.current === target) {
          pendingExternalHashNavigationRef.current = '';
        }
      };
      const navigateToTarget = () => {
        allowNavigationTo(target);
        navigate(target, { replace: true });
      };

      if (!configurationEditor.dirty && !configurationSaving) {
        navigateToTarget();
        finish();
        return;
      }

      void confirmConfigurationDiscard()
        .then((allowed) => {
          if (allowed) navigateToTarget();
        })
        .finally(finish);
    };

    window.addEventListener('popstate', interceptExternalHashNavigation, true);
    window.addEventListener('hashchange', interceptExternalHashNavigation, true);
    return () => {
      window.removeEventListener('popstate', interceptExternalHashNavigation, true);
      window.removeEventListener('hashchange', interceptExternalHashNavigation, true);
    };
  }, [
    allowNavigationTo,
    configurationEditor.dirty,
    configurationSaving,
    confirmConfigurationDiscard,
    location.hash,
    location.pathname,
    location.search,
    navigate,
  ]);

  const changeActiveView = useCallback(
    async (view: AccountsView) => {
      const hadDirtyConfiguration = configurationEditor.dirty;
      if (!(await confirmConfigurationDiscard())) return;
      setActiveView(view);
      const searchValue = writeAccountsWorkspaceUrlSearch(
        location.search,
        { ...workspaceUrlState, view },
        DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE
      );
      lastWorkspaceNavigationRef.current = searchValue;
      if (hadDirtyConfiguration) allowNextNavigation();
      navigate(
        {
          pathname: location.pathname,
          search: searchValue,
        },
        { replace: false }
      );
    },
    [
      allowNextNavigation,
      configurationEditor.dirty,
      confirmConfigurationDiscard,
      location.pathname,
      location.search,
      navigate,
      workspaceUrlState,
    ]
  );

  const changeHealthMode = useCallback(
    (mode: CredentialHealthInspectionMode) => {
      setHealthMode(mode);
      const searchValue = writeAccountsWorkspaceUrlSearch(
        location.search,
        { ...workspaceUrlState, healthMode: mode },
        DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE
      );
      lastWorkspaceNavigationRef.current = searchValue;
      navigate({ pathname: location.pathname, search: searchValue }, { replace: true });
    },
    [location.pathname, location.search, navigate, workspaceUrlState]
  );

  const handleInspectionCodexReauthStart = useCallback(
    (target: CodexReauthTarget): boolean => {
      const targetIdentityKey = getAuthFileCodexInspectionKeyForIdentity({
        fileName: target.fileName ?? '',
        runtimeId: target.runtimeId,
        provider: target.provider,
        authIndex: target.authIndex ?? null,
        accountId: target.accountId,
        accountSnapshot: target.accountSnapshot,
      });
      const targetFile = files.find(
        (file) => getAuthFileCodexInspectionKeyForFile(file) === targetIdentityKey
      );
      if (!targetFile) {
        inspectionCodexReauthBaselineRef.current = null;
        showNotification(t('notification.refresh_failed'), 'error');
        return false;
      }
      const baseline = createAccountDirectReauthBaseline({
        target,
        file: targetFile,
        files,
        resultKeys: getHandledAccountInspectionResultKeys(
          inspectionResults,
          targetIdentityKey,
          target.fileName ?? '',
          files
        ),
      });
      if (!baseline) {
        inspectionCodexReauthBaselineRef.current = null;
        showNotification(t('notification.refresh_failed'), 'error');
        return false;
      }
      inspectionCodexReauthBaselineRef.current = {
        scopeKey: credentialEvidenceScopeKey,
        target,
        baseline,
      };
      return true;
    },
    [credentialEvidenceScopeKey, files, inspectionResults, showNotification, t]
  );

  const handleInspectionCredentialsChanged = useCallback(
    async (target?: CodexReauthTarget | null, snapshot?: CredentialInspectionSnapshot | null) => {
      const callbackScopeKey = credentialEvidenceScopeKey;
      if (activeCredentialEvidenceScopeKeyRef.current !== callbackScopeKey) return;
      if (target) {
        const captured = inspectionCodexReauthBaselineRef.current;
        if (!captured || captured.scopeKey !== callbackScopeKey || captured.target !== target) {
          throw new Error(t('notification.refresh_failed'));
        }
        inspectionCodexReauthBaselineRef.current = null;
        const pending = recordPendingAccountDirectReauth({
          connectionFingerprint,
          baseline: captured.baseline,
        });
        if (!pending) throw new Error(t('notification.refresh_failed'));
        const reconciliationBatch = await reconcilePendingAccountDirectReauthsWithRetry([pending], {
          reload: true,
        });
        if (!reconciliationBatch) return;
        const reconciliation =
          reconciliationBatch.reconciliations.get(pending.id) ??
          ({ status: 'unconfirmed' } as const);
        if (reconciliation.status === 'confirmed') {
          completeConfirmedAccountDirectReauth(
            pending,
            reconciliation.file,
            reconciliationBatch.files,
            pending.id
          );
          return;
        }
        if (reconciliation.status !== 'unconfirmed') {
          acknowledgePendingAccountDirectReauths([pending.id]);
        }
        const code =
          reconciliation.status === 'identity-changed'
            ? 'identity_changed'
            : reconciliation.status === 'ambiguous'
              ? 'identity_ambiguous'
              : 'identity_unconfirmed';
        throw new CodexReauthReconciliationError(code, t(`codex_reauth.${code}`));
        return;
      }
      if (snapshot) {
        const scopedKey = getInspectionCredentialMutationScopedKey(snapshot, false);
        if (!scopedKey) return;
        const synchronized = await synchronizeInspectionCredentialMutations(snapshot, false);
        if (activeCredentialEvidenceScopeKeyRef.current !== callbackScopeKey) return;
        if (synchronized || synchronizedInspectionMutationKeysRef.current.has(scopedKey)) return;
        if (pendingInspectionMutationSnapshotsRef.current.has(scopedKey)) {
          throw new Error(t('notification.refresh_failed'));
        }
        return;
      }
      await reloadInspectionCredentialArtifacts({ requireSuccessfulReload: true });
    },
    [
      completeConfirmedAccountDirectReauth,
      connectionFingerprint,
      credentialEvidenceScopeKey,
      getInspectionCredentialMutationScopedKey,
      reloadInspectionCredentialArtifacts,
      reconcilePendingAccountDirectReauthsWithRetry,
      synchronizeInspectionCredentialMutations,
      t,
    ]
  );

  const handleInspectionSnapshotChange = useCallback(
    (snapshot: CredentialInspectionSnapshot) => {
      if (activeCredentialEvidenceScopeKeyRef.current !== credentialEvidenceScopeKey) return;
      capturePendingInspectionEvidenceBaselineRef.current(snapshot);
      const currentSnapshot = inspectionSnapshotRef.current;
      if (!currentSnapshot || snapshot.completedAtMs >= currentSnapshot.completedAtMs) {
        inspectionSnapshotRef.current = snapshot;
      }
      applyInspectionSnapshot(snapshot);
      void synchronizeInspectionCredentialMutations(snapshot, true);
    },
    [applyInspectionSnapshot, credentialEvidenceScopeKey, synchronizeInspectionCredentialMutations]
  );

  const handleOpenInspectionCredential = useCallback(
    async (target: CredentialInspectionTarget) => {
      const targetRow = findAccountRowForInspectionTarget(rows, target);
      if (!targetRow) {
        showNotification(t('accounts.inspection_credential_not_found'), 'warning');
        return;
      }
      const hadDirtyConfiguration = configurationEditor.dirty;
      if (!(await confirmConfigurationDiscard())) return;

      setActiveView('accounts');
      setSelectedRowKey(targetRow.selectionKey);
      setDetailTab('diagnostics');
      const searchValue = writeAccountsWorkspaceUrlSearch(
        location.search,
        {
          ...workspaceUrlState,
          view: 'accounts',
          account: targetRow.selectionKey,
          detailTab: 'diagnostics',
        },
        DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE
      );
      lastWorkspaceNavigationRef.current = searchValue;
      if (hadDirtyConfiguration) allowNextNavigation();
      navigate({ pathname: location.pathname, search: searchValue }, { replace: false });
    },
    [
      allowNextNavigation,
      configurationEditor.dirty,
      confirmConfigurationDiscard,
      location.pathname,
      location.search,
      navigate,
      rows,
      showNotification,
      t,
      workspaceUrlState,
    ]
  );

  useEffect(() => {
    if (!selectedRowKey) {
      setDetailTab('overview');
    }
  }, [selectedRowKey]);

  useLayoutEffect(() => {
    selectedRowKeyRef.current = selectedRowKey;
    accountHistoryTargetAbortRef.current?.abort();
    accountHistoryTargetAbortRef.current = null;
    accountHistoryRefreshRequestIdRef.current += 1;
    accountHistoryRefreshPromiseRef.current = null;
    setHistoryRefreshing(false);
    usageValuesRequestIdRef.current += 1;
    usageValuesAutoLoadKeyRef.current = null;
    accountWindowUsageAutoLoadKeyRef.current = null;
    setAccountWindowUsageQueryContext(null);
    detailEventsRequestIdRef.current += 1;
    detailEventsAutoLoadKeyRef.current = null;
    setDetailEventsLoading(false);
    setDetailEventsAppending(false);
  }, [selectedRowKey]);

  useLayoutEffect(() => {
    if (detailDrawerBodyRef.current) {
      detailDrawerBodyRef.current.scrollTop = 0;
    }
  }, [detailTab, selectedRowKey]);

  useEffect(() => {
    if (loading || error || !selectedRowKey || selectedRow) return;
    setSelectedRowKey(null);
    setDetailTab('overview');
  }, [error, loading, selectedRow, selectedRowKey]);

  const loadAccountWindowUsage = useCallback(async () => {
    const requestId = accountWindowUsageReqIdRef.current + 1;
    accountWindowUsageReqIdRef.current = requestId;
    accountWindowUsageAbortRef.current?.abort();
    accountWindowUsageAbortRef.current = null;

    if (featureAvailability.checking || !managerStorageAvailable || !selectedRow) {
      setAccountWindowUsageByKey(new Map());
      setAccountWindowUsageQueryContext(null);
      setAccountWindowUsageError('');
      return;
    }

    const controller = new AbortController();
    accountWindowUsageAbortRef.current = controller;
    const queryAsOfMs = Date.now();
    const initialDefinitions =
      effectiveQuotaWindowDefinitionsByRowKey.get(selectedRow.selectionKey) ??
      buildAccountQuotaWindowDefinitions(buildQuotaDisplayWindows(selectedRow));
    let entries = buildAccountWindowUsageTargetEntries(
      [selectedRow],
      new Map([[selectedRow.selectionKey, initialDefinitions]]),
      queryAsOfMs
    );
    const isCurrentRequest = () =>
      accountWindowUsageReqIdRef.current === requestId && !controller.signal.aborted;
    setAccountWindowUsageError('');
    try {
      const localDefinitions = quotaWindowDefinitionsByRowKey.get(selectedRow.selectionKey) ?? [];
      let writeEntries: AccountQuotaSnapshotWriteEntry[] = [];
      if (selectedRow.provider === CODEX_CONFIG.type) {
        const activeQuota = getActiveCodexQuota(selectedRow.raw);
        const apiObservation = getQuotaSnapshotObservation(selectedRow);
        if (apiObservation) {
          const apiDefinitions = buildCodexSnapshotDefinitions(selectedRow, activeQuota);
          writeEntries.push(
            ...buildAccountQuotaSnapshotWriteEntries(
              [selectedRow],
              new Map([[selectedRow.selectionKey, apiDefinitions]]),
              {
                getCodexQuota: () => activeQuota,
                getObservation: () => apiObservation,
              }
            )
          );
        }

        const headerObservation = getCodexHeaderQuotaSnapshotObservation(selectedRow);
        if (headerObservation) {
          const headerQuota = buildObservedCodexQuotaState(
            selectedRow.raw,
            getEffectiveCodexHeaderSnapshot(selectedRow.raw),
            t,
            headerSnapshotGeneratedAtMs
          );
          const headerDefinitions = buildCodexSnapshotDefinitions(selectedRow, headerQuota);
          writeEntries.push(
            ...buildAccountQuotaSnapshotWriteEntries(
              [selectedRow],
              new Map([[selectedRow.selectionKey, headerDefinitions]]),
              {
                getCodexQuota: () => headerQuota,
                getObservation: () => headerObservation,
              }
            )
          );
        }
      } else {
        writeEntries = buildAccountQuotaSnapshotWriteEntries(
          [selectedRow],
          new Map([[selectedRow.selectionKey, localDefinitions]]),
          { getObservation: getQuotaSnapshotObservation }
        );
      }
      if (writeEntries.length > 0) {
        try {
          await accountQuotaSnapshotApi.write(
            featureAvailability.managerServiceBase,
            managementKey,
            writeEntries,
            controller.signal
          );
        } catch {
          if (controller.signal.aborted) return;
          // Snapshot persistence is additive. A write failure must not block
          // reading previously persisted lifecycle evidence.
        }
      }
      const queryAccounts = buildAccountQuotaSnapshotQueryAccounts([selectedRow]);
      if (queryAccounts.length > 0) {
        try {
          const snapshotResponse = await accountQuotaSnapshotApi.query(
            featureAvailability.managerServiceBase,
            managementKey,
            queryAccounts,
            { includeInactive: true },
            controller.signal
          );
          if (!isCurrentRequest()) return;
          const snapshotWindows = snapshotResponse.items.find(
            (item) => item.row_key === selectedRow.selectionKey
          )?.windows;
          if (snapshotWindows) {
            setQuotaSnapshotWindowsByRowKey((current) => {
              const next = new Map(current);
              next.set(selectedRow.selectionKey, snapshotWindows);
              return next;
            });
            const mergedDefinitions = mergeAccountQuotaSnapshotWindows(
              localDefinitions,
              snapshotWindows,
              { provider: selectedRow.provider }
            );
            entries = buildAccountWindowUsageTargetEntries(
              [selectedRow],
              new Map([[selectedRow.selectionKey, mergedDefinitions]]),
              queryAsOfMs
            );
          }
        } catch {
          if (controller.signal.aborted) return;
          // Older Manager Server versions or transient query failures must not
          // block the existing usage query path.
        }
      }
      if (!featureAvailability.requestMonitoringAvailable) {
        if (!isCurrentRequest()) return;
        setAccountWindowUsageByKey(new Map());
        setAccountWindowUsageQueryContext(null);
        setAccountWindowUsageError('');
        return;
      }
      if (entries.length === 0) {
        if (!isCurrentRequest()) return;
        setAccountWindowUsageByKey(new Map());
        return;
      }
      const response = await monitoringAnalyticsApi.getAccountWindowUsage(
        featureAvailability.managerServiceBase,
        managementKey,
        {
          windows: entries.map((entry) => entry.target),
        },
        controller.signal
      );
      if (!isCurrentRequest()) return;
      setAccountWindowUsageQueryContext({
        rowKey: selectedRow.selectionKey,
        asOfMs: queryAsOfMs,
      });
      setAccountWindowUsageByKey(buildAccountWindowUsageByKey(entries, response.items ?? []));
    } catch (err: unknown) {
      if (!isCurrentRequest()) return;
      setAccountWindowUsageError(
        err instanceof Error ? err.message : t('notification.load_failed')
      );
    } finally {
      if (accountWindowUsageReqIdRef.current === requestId) {
        if (accountWindowUsageAbortRef.current === controller) {
          accountWindowUsageAbortRef.current = null;
        }
      }
    }
  }, [
    buildCodexSnapshotDefinitions,
    buildQuotaDisplayWindows,
    effectiveQuotaWindowDefinitionsByRowKey,
    featureAvailability.checking,
    featureAvailability.managerServiceBase,
    featureAvailability.requestMonitoringAvailable,
    getActiveCodexQuota,
    getCodexHeaderQuotaSnapshotObservation,
    getEffectiveCodexHeaderSnapshot,
    getQuotaSnapshotObservation,
    headerSnapshotGeneratedAtMs,
    managementKey,
    managerStorageAvailable,
    quotaWindowDefinitionsByRowKey,
    selectedRow,
    t,
  ]);

  const loadUsageValues = useCallback(async () => {
    if (!selectedRow) return;
    const requestId = usageValuesRequestIdRef.current + 1;
    usageValuesRequestIdRef.current = requestId;
    const commitRows = (rows: UsageValueRow[]) => {
      if (usageValuesRequestIdRef.current !== requestId) return;
      setUsageRows(rows);
    };
    const fallback = () => {
      commitRows(buildUsageValueRowsFromRecent([selectedRow]));
    };

    if (
      featureAvailability.checking ||
      !featureAvailability.requestMonitoringAvailable ||
      !featureAvailability.managerServiceBase ||
      !managementKey
    ) {
      fallback();
      return;
    }

    try {
      const toMs = Date.now();
      const authIndex = selectedRow.authIndex ? String(selectedRow.authIndex) : '';
      const response = await monitoringAnalyticsApi.getAnalytics(
        featureAvailability.managerServiceBase,
        managementKey,
        {
          from_ms: toMs - ACCOUNT_OVERVIEW_ACTIVITY_RANGE_MS,
          to_ms: toMs,
          now_ms: toMs,
          filters: {
            auth_files: [selectedRow.fileName],
            ...(authIndex ? { auth_indices: [authIndex] } : {}),
          },
          include: {
            summary: true,
            account_stats: true,
          },
        }
      );
      const stats: MonitoringAnalyticsAccountStatRow[] = response.account_stats ?? [];
      const monitoringRow = buildUsageValueRowFromMonitoringSummary(
        selectedRow,
        response.summary,
        stats
      );
      commitRows([monitoringRow]);
    } catch {
      fallback();
    }
  }, [
    featureAvailability.checking,
    featureAvailability.managerServiceBase,
    featureAvailability.requestMonitoringAvailable,
    managementKey,
    selectedRow,
  ]);

  useEffect(() => {
    if (activeView !== 'accounts' || detailTab !== 'overview' || !selectedRowKey) return;
    if (usageValuesAutoLoadKeyRef.current === usageValuesAutoLoadKey) return;
    usageValuesAutoLoadKeyRef.current = usageValuesAutoLoadKey;
    void loadUsageValues();
  }, [activeView, detailTab, loadUsageValues, selectedRowKey, usageValuesAutoLoadKey]);

  const loadAccountHistory = useCallback(
    async (targetEntries?: AccountHistoryTargetEntry[]) => {
      const entries = targetEntries ?? accountHistoryTargets;
      const mergeResult = targetEntries !== undefined;
      const controllerRef = mergeResult ? accountHistoryTargetAbortRef : accountHistoryAutoAbortRef;
      const managerServiceBase = featureAvailability.managerServiceBase;
      if (!mergeResult) {
        const activeRowKeys = new Set(entries.map((entry) => entry.rowKey));
        accountHistoryRequestVersionsRef.current = retainAccountHistoryRowKeys(
          accountHistoryRequestVersionsRef.current,
          activeRowKeys
        );
        setAccountHistoryByRowKey((current) => retainAccountHistoryRowKeys(current, activeRowKeys));
        setAccountHistoryErrorsByRowKey((current) =>
          retainAccountHistoryRowKeys(current, activeRowKeys)
        );
      }
      if (
        !mergeResult &&
        controllerRef.current &&
        accountHistoryAutoRequestContextKeyRef.current === accountHistoryAutoContextKey
      ) {
        return;
      }
      controllerRef.current?.abort();
      controllerRef.current = null;
      if (!mergeResult) {
        accountHistoryAutoRequestContextKeyRef.current = null;
      }

      if (
        featureAvailability.checking ||
        !featureAvailability.requestMonitoringAvailable ||
        !managerServiceBase ||
        !managementKey ||
        entries.length === 0
      ) {
        if (!mergeResult) {
          setAccountHistoryByRowKey(new Map());
        }
        setAccountHistoryLoading(accountHistoryPendingRequestsRef.current.size > 0);
        setAccountHistoryErrorsByRowKey((current) => {
          if (!mergeResult) return current.size === 0 ? current : new Map();
          const next = new Map(current);
          entries.forEach((entry) => next.delete(entry.rowKey));
          return next.size === current.size ? current : next;
        });
        return;
      }

      const controller = new AbortController();
      controllerRef.current = controller;
      if (!mergeResult) {
        accountHistoryAutoRequestContextKeyRef.current = accountHistoryAutoContextKey;
      }
      const requestToken = Symbol('account-history-request');
      accountHistoryPendingRequestsRef.current.add(requestToken);
      const requestVersions = new Map<string, symbol>();
      entries.forEach((entry) => {
        const version = Symbol(entry.rowKey);
        accountHistoryRequestVersionsRef.current.set(entry.rowKey, version);
        requestVersions.set(entry.rowKey, version);
      });
      setAccountHistoryLoading(true);
      setAccountHistoryErrorsByRowKey((current) => {
        const next = new Map(current);
        entries.forEach((entry) => next.delete(entry.rowKey));
        return next.size === current.size ? current : next;
      });
      try {
        const batches = buildAccountHistoryTargetBatches(entries);
        const batchResults = await mapWithConcurrency(
          batches,
          MAX_CONCURRENT_ACCOUNT_HISTORY_REQUESTS,
          async (batch) => {
            try {
              const response = await monitoringAnalyticsApi.getAccountHistory(
                managerServiceBase,
                managementKey,
                {
                  accounts: batch.map((entry) => entry.target),
                },
                controller.signal
              );
              return { batch, response, error: '' };
            } catch (err: unknown) {
              if (controller.signal.aborted) throw err;
              return {
                batch,
                response: null,
                error: err instanceof Error ? err.message : t('notification.load_failed'),
              };
            }
          }
        );
        if (controller.signal.aborted) return;
        const nextHistory = new Map<string, MonitoringAccountHistoryItem>();
        const failedRows = new Map<string, string>();
        batchResults.forEach(({ batch, response, error }) => {
          if (!response) {
            batch.forEach((entry) => failedRows.set(entry.rowKey, error));
            return;
          }
          buildAccountHistoryByRowKey(batch, response.items, response.generated_at_ms).forEach(
            (item, rowKey) => nextHistory.set(rowKey, item)
          );
        });
        setAccountHistoryByRowKey((current) => {
          const merged = mergeResult
            ? new Map(current)
            : new Map<string, MonitoringAccountHistoryItem>();
          entries.forEach((entry) => {
            const requestVersion = requestVersions.get(entry.rowKey);
            if (accountHistoryRequestVersionsRef.current.get(entry.rowKey) !== requestVersion) {
              if (!mergeResult) {
                const currentItem = current.get(entry.rowKey);
                if (currentItem) merged.set(entry.rowKey, currentItem);
              }
              return;
            }
            if (failedRows.has(entry.rowKey)) {
              if (!mergeResult) {
                const currentItem = current.get(entry.rowKey);
                if (currentItem) merged.set(entry.rowKey, currentItem);
              }
              return;
            }
            merged.delete(entry.rowKey);
            const nextItem = nextHistory.get(entry.rowKey);
            if (nextItem) merged.set(entry.rowKey, nextItem);
          });
          return merged;
        });
        setAccountHistoryErrorsByRowKey((current) => {
          const next = new Map(current);
          entries.forEach((entry) => {
            const requestVersion = requestVersions.get(entry.rowKey);
            if (accountHistoryRequestVersionsRef.current.get(entry.rowKey) !== requestVersion) {
              return;
            }
            const error = failedRows.get(entry.rowKey);
            if (error) {
              next.set(entry.rowKey, error);
            } else {
              next.delete(entry.rowKey);
            }
          });
          return next;
        });
      } catch (err: unknown) {
        const requestWasAborted = controller.signal.aborted;
        if (!requestWasAborted) controller.abort();
        if (requestWasAborted) return;
        const message = err instanceof Error ? err.message : t('notification.load_failed');
        setAccountHistoryErrorsByRowKey((current) => {
          const next = new Map(current);
          entries.forEach((entry) => {
            const requestVersion = requestVersions.get(entry.rowKey);
            if (accountHistoryRequestVersionsRef.current.get(entry.rowKey) === requestVersion) {
              next.set(entry.rowKey, message);
            }
          });
          return next;
        });
      } finally {
        if (controllerRef.current === controller) {
          controllerRef.current = null;
          if (!mergeResult) {
            accountHistoryAutoRequestContextKeyRef.current = null;
          }
        }
        accountHistoryPendingRequestsRef.current.delete(requestToken);
        setAccountHistoryLoading(accountHistoryPendingRequestsRef.current.size > 0);
      }
    },
    [
      accountHistoryAutoContextKey,
      accountHistoryTargets,
      featureAvailability.checking,
      featureAvailability.managerServiceBase,
      featureAvailability.requestMonitoringAvailable,
      managementKey,
      t,
    ]
  );

  useEffect(() => {
    if (activeView !== 'accounts') {
      accountHistoryAutoLoadKeyRef.current = null;
      accountHistoryAutoAbortRef.current?.abort();
      accountHistoryAutoAbortRef.current = null;
      accountHistoryAutoRequestContextKeyRef.current = null;
      return;
    }
    if (accountHistoryAutoLoadKeyRef.current === accountHistoryAutoLoadKey) return;
    accountHistoryAutoLoadKeyRef.current = accountHistoryAutoLoadKey;
    void loadAccountHistory();
  }, [accountHistoryAutoLoadKey, activeView, loadAccountHistory]);

  const loadDetailEvents = useCallback(
    async (
      row: AccountRow,
      options: { append?: boolean; beforeMs?: number | null; beforeId?: number | null } = {}
    ) => {
      const append = options.append === true;
      const requestId = detailEventsRequestIdRef.current + 1;
      detailEventsRequestIdRef.current = requestId;
      const shouldCommit = () => detailEventsRequestIdRef.current === requestId;

      setDetailEventsRowKey(row.selectionKey);
      if (!append) {
        setDetailEvents([]);
        setDetailEventsSummary(null);
        setDetailEventsRecentFailure(null);
        setDetailEventsTotalCount(0);
        setDetailEventsHasMore(false);
        setDetailEventsNextBeforeMs(null);
        setDetailEventsNextBeforeId(null);
      }
      setDetailEventsError('');

      if (
        featureAvailability.checking ||
        !featureAvailability.requestMonitoringAvailable ||
        !featureAvailability.managerServiceBase ||
        !managementKey
      ) {
        setDetailEventsLoading(false);
        return;
      }

      if (append) setDetailEventsAppending(true);
      else setDetailEventsLoading(true);
      try {
        const toMs = Date.now();
        const authIndex = row.authIndex ? String(row.authIndex) : '';
        const response = await monitoringAnalyticsApi.getAnalytics(
          featureAvailability.managerServiceBase,
          managementKey,
          {
            from_ms: toMs - DETAIL_EVENTS_RANGE_MS,
            to_ms: toMs,
            now_ms: toMs,
            filters: {
              auth_files: [row.fileName],
              ...(authIndex ? { auth_indices: [authIndex] } : {}),
            },
            include: {
              summary: true,
              summary_profile: 'compact',
              summary_percentiles: true,
              recent_failures: 1,
              events_page: {
                limit: DETAIL_EVENTS_LIMIT,
                before_ms: options.beforeMs ?? null,
                before_id: options.beforeId ?? null,
              },
              granularity: 'day',
            },
          }
        );
        if (!shouldCommit()) return;
        const eventsPage = response.events;
        const nextItems = eventsPage?.items ?? [];
        setDetailEvents((current) => (append ? [...current, ...nextItems] : nextItems));
        setDetailEventsSummary((current) => response.summary ?? (append ? current : null));
        setDetailEventsRecentFailure(
          (current) => response.recent_failures?.[0] ?? (append ? current : null)
        );
        setDetailEventsTotalCount(
          (current) =>
            eventsPage?.total_count ?? (append ? current + nextItems.length : nextItems.length)
        );
        setDetailEventsHasMore(eventsPage?.has_more === true);
        setDetailEventsNextBeforeMs(
          typeof eventsPage?.next_before_ms === 'number' && eventsPage.next_before_ms > 0
            ? eventsPage.next_before_ms
            : null
        );
        setDetailEventsNextBeforeId(
          typeof eventsPage?.next_before_id === 'number' && eventsPage.next_before_id > 0
            ? eventsPage.next_before_id
            : null
        );
      } catch (err: unknown) {
        if (!shouldCommit()) return;
        setDetailEventsError(err instanceof Error ? err.message : t('notification.load_failed'));
      } finally {
        if (shouldCommit()) {
          if (append) setDetailEventsAppending(false);
          else setDetailEventsLoading(false);
        }
      }
    },
    [
      featureAvailability.checking,
      featureAvailability.managerServiceBase,
      featureAvailability.requestMonitoringAvailable,
      managementKey,
      t,
    ]
  );

  const refreshDetailEvents = useCallback(
    async (row: AccountRow) => {
      setDetailEventsRefreshing(true);
      try {
        await loadDetailEvents(row);
      } finally {
        setDetailEventsRefreshing(false);
      }
    },
    [loadDetailEvents]
  );

  useEffect(() => {
    if (detailTab !== 'diagnostics' || !selectedRow) return;
    if (detailEventsAutoLoadKeyRef.current === detailEventsAutoLoadKey) return;
    detailEventsAutoLoadKeyRef.current = detailEventsAutoLoadKey;
    void loadDetailEvents(selectedRow);
  }, [detailEventsAutoLoadKey, detailTab, loadDetailEvents, selectedRow]);

  const refreshQuotaForRow = useCallback(
    async (row: AccountRow) => {
      if (row.runtimeOnly) return false;
      const refreshWithConfig = <TState, TData>(
        config: QuotaConfig<TState, TData>,
        setQuota: QuotaSetter<TState>,
        currentState?: TState
      ) => {
        const storeKey = config.getStoreKey?.(row.raw) ?? row.fileName;
        return refreshQuotaWithConfig({
          config,
          file: row.raw,
          setQuota,
          t,
          isCurrent: beginAccountQuotaRequest(
            quotaRequestVersionsRef.current,
            `${config.type}:${storeKey}`
          ),
          requestScope: authFilesRequestScope,
          currentState,
        });
      };
      switch (row.provider) {
        case CODEX_CONFIG.type: {
          const result = await refreshWithConfig(
            CODEX_CONFIG,
            setCodexQuota,
            getScopedQuotaState(CODEX_CONFIG, baseQuotaStores.codexQuota, row.raw)
          );
          if (!result || result.status !== 'success') return false;
          const refreshedQuota = result.state;
          const healthyQuota = isKnownHealthyCodexQuota(refreshedQuota);
          invalidateCodexCredentialStatusForSelectionKeys([row.selectionKey], {
            supersedeAuthenticationActionEvidence: true,
            supersedeQuotaActionEvidence: healthyQuota,
            supersedeCooldownEvidence: healthyQuota,
          });
          return true;
        }
        case CLAUDE_CONFIG.type:
          return (
            (
              await refreshWithConfig(
                CLAUDE_CONFIG,
                setClaudeQuota,
                getScopedQuotaState(CLAUDE_CONFIG, baseQuotaStores.claudeQuota, row.raw)
              )
            )?.status === 'success'
          );
        case ANTIGRAVITY_CONFIG.type:
          return (
            (
              await refreshWithConfig(
                ANTIGRAVITY_CONFIG,
                setAntigravityQuota,
                getScopedQuotaState(ANTIGRAVITY_CONFIG, baseQuotaStores.antigravityQuota, row.raw)
              )
            )?.status === 'success'
          );
        case KIMI_CONFIG.type:
          return (
            (
              await refreshWithConfig(
                KIMI_CONFIG,
                setKimiQuota,
                getScopedQuotaState(KIMI_CONFIG, baseQuotaStores.kimiQuota, row.raw)
              )
            )?.status === 'success'
          );
        case XAI_CONFIG.type:
          return (
            (
              await refreshWithConfig<XaiQuotaState, NonNullable<XaiQuotaState['billing']>>(
                XAI_CONFIG,
                setXaiQuota,
                getScopedQuotaState(XAI_CONFIG, baseQuotaStores.xaiQuota, row.raw)
              )
            )?.status === 'success'
          );
        default:
          return false;
      }
    },
    [
      invalidateCodexCredentialStatusForSelectionKeys,
      setAntigravityQuota,
      setClaudeQuota,
      setCodexQuota,
      setKimiQuota,
      setXaiQuota,
      t,
      authFilesRequestScope,
      baseQuotaStores,
    ]
  );

  const refreshQuotaRows = useCallback(
    (targets: AccountRow[]): Promise<void> => {
      const currentBatch = quotaRefreshBatchRef.current;
      if (currentBatch?.connectionFingerprint === connectionFingerprint) {
        return currentBatch.promise;
      }
      const refreshable = targets.filter((row) => !row.runtimeOnly);
      if (refreshable.length === 0) {
        showNotification(t('accounts.no_refreshable_accounts'), 'warning');
        return Promise.resolve();
      }
      const taskPlan = buildProviderCredentialTaskPlan(refreshable, {
        getProviderKey: (row) => row.provider,
        getCredentialKey: (row) => getQuotaCredentialStoreKey(row.raw),
      });
      const generation = quotaRefreshGenerationRef.current;
      const isCurrentBatch = () =>
        quotaRefreshGenerationRef.current === generation &&
        oauthEditorConnectionFingerprintRef.current === connectionFingerprint;
      const batchPromise = (async () => {
        setQuotaRefreshing(true);
        try {
          const results = await runProviderCredentialTaskPlan(
            taskPlan,
            {
              perProviderConcurrency: MAX_CONCURRENT_QUOTA_REFRESHES_PER_PROVIDER,
              maxConcurrentProviders: MAX_CONCURRENT_QUOTA_REFRESH_PROVIDERS,
            },
            ({ item }) => (isCurrentBatch() ? refreshQuotaForRow(item) : Promise.resolve(false))
          );
          if (!isCurrentBatch()) return;
          const successCount = results.filter(Boolean).length;
          showNotification(
            t('accounts.quota_refresh_result', {
              success: successCount,
              total: taskPlan.length,
            }),
            successCount === taskPlan.length ? 'success' : 'warning'
          );
        } finally {
          if (
            quotaRefreshBatchRef.current?.generation === generation &&
            quotaRefreshBatchRef.current.connectionFingerprint === connectionFingerprint
          ) {
            quotaRefreshBatchRef.current = null;
            setQuotaRefreshing(false);
          }
        }
      })();
      quotaRefreshBatchRef.current = { connectionFingerprint, generation, promise: batchPromise };
      return batchPromise;
    },
    [connectionFingerprint, refreshQuotaForRow, showNotification, t]
  );

  const refreshAccountQuota = useCallback(
    async (row: AccountRow): Promise<void> => {
      await refreshQuotaRows([row]);
      if (selectedRowKeyRef.current === row.selectionKey) {
        setAccountQuotaRefreshRevision((current) => current + 1);
      }
    },
    [refreshQuotaRows]
  );

  const refreshAccountHistory = useCallback(
    (row: AccountRow): Promise<void> => {
      if (!requestHistoryAvailable) return Promise.resolve();
      const refreshKey = JSON.stringify({
        checking: featureAvailability.checking,
        connectionFingerprint,
        managerConnectionFingerprint,
        requestMonitoringAvailable: featureAvailability.requestMonitoringAvailable,
        rowKey: row.selectionKey,
      });
      const currentRefresh = accountHistoryRefreshPromiseRef.current;
      if (currentRefresh?.key === refreshKey) {
        return currentRefresh.promise;
      }
      const requestId = accountHistoryRefreshRequestIdRef.current + 1;
      accountHistoryRefreshRequestIdRef.current = requestId;
      const refreshPromise = (async () => {
        const isCurrentContext = () => {
          const context = headerSnapshotContextRef.current;
          return (
            accountHistoryRefreshRequestIdRef.current === requestId &&
            context.managerServiceBase === featureAvailability.managerServiceBase &&
            context.managementKey === managementKey &&
            context.connectionFingerprint === connectionFingerprint &&
            context.checking === featureAvailability.checking &&
            context.requestMonitoringAvailable === featureAvailability.requestMonitoringAvailable &&
            selectedRowKeyRef.current === row.selectionKey
          );
        };
        setHistoryRefreshing(true);
        try {
          await loadAccountHistory(buildAccountHistoryTargetEntries([row]));
          if (!isCurrentContext()) return;
          if (row.provider === CODEX_CONFIG.type) {
            await loadHeaderSnapshots();
            if (!isCurrentContext()) return;
          }
          setAccountHistoryRefreshRevision((current) => current + 1);
        } finally {
          if (accountHistoryRefreshRequestIdRef.current === requestId) {
            accountHistoryRefreshPromiseRef.current = null;
            setHistoryRefreshing(false);
          }
        }
      })();
      accountHistoryRefreshPromiseRef.current = { key: refreshKey, promise: refreshPromise };
      return refreshPromise;
    },
    [
      connectionFingerprint,
      featureAvailability.checking,
      featureAvailability.managerServiceBase,
      featureAvailability.requestMonitoringAvailable,
      loadAccountHistory,
      loadHeaderSnapshots,
      managementKey,
      managerConnectionFingerprint,
      requestHistoryAvailable,
    ]
  );

  useEffect(() => {
    if (activeView !== 'accounts' || detailTab !== 'quota' || !selectedRowKey || !selectedRow) {
      const hadInFlightRequest = accountWindowUsageAbortRef.current !== null;
      accountWindowUsageReqIdRef.current += 1;
      accountWindowUsageAbortRef.current?.abort();
      accountWindowUsageAbortRef.current = null;
      if (hadInFlightRequest || !selectedRow) {
        accountWindowUsageAutoLoadKeyRef.current = null;
      }
      return;
    }
    if (
      canLoadHeaderSnapshots &&
      selectedRow?.provider === CODEX_CONFIG.type &&
      headerSnapshotLoadedKey !== headerSnapshotLoadKey
    ) {
      return;
    }
    if (accountWindowUsageAutoLoadKeyRef.current === accountWindowUsageAutoLoadKey) return;
    accountWindowUsageAutoLoadKeyRef.current = accountWindowUsageAutoLoadKey;
    void loadAccountWindowUsage();
  }, [
    accountWindowUsageAutoLoadKey,
    activeView,
    canLoadHeaderSnapshots,
    detailTab,
    headerSnapshotLoadKey,
    headerSnapshotLoadedKey,
    loadAccountWindowUsage,
    selectedRow,
    selectedRowKey,
  ]);

  const canResetCodexQuota = useCallback(
    (row: AccountRow) => {
      if (row.provider !== CODEX_CONFIG.type || row.runtimeOnly) return false;
      if (normalizeAuthIndex(row.raw['auth_index'] ?? row.raw.authIndex) === null) return false;
      const evidence = getDisplayCodexResetEvidenceQuota(row);
      return (
        (evidence?.rateLimitResetCreditsAvailableCount ?? 0) > 0 ||
        (evidence?.rateLimitResetCredits?.length ?? 0) > 0
      );
    },
    [getDisplayCodexResetEvidenceQuota]
  );

  const resetCodexQuotaForRow = useCallback(
    (row: AccountRow) => {
      if (!canResetCodexQuota(row)) return;
      const storeKey = CODEX_CONFIG.getStoreKey?.(row.raw) ?? row.fileName;
      const requestKey = `${CODEX_CONFIG.type}:${storeKey}`;
      if (resettingQuotaKeysRef.current.has(requestKey)) return;
      resettingQuotaKeysRef.current.add(requestKey);
      setResettingQuotaKeys((prev) => new Set(prev).add(requestKey));
      const endResetTransaction = () => {
        if (!resettingQuotaKeysRef.current.delete(requestKey)) return;
        setResettingQuotaKeys((prev) => {
          if (!prev.has(requestKey)) return prev;
          const next = new Set(prev);
          next.delete(requestKey);
          return next;
        });
      };
      const displayName = getDisplayAccount(row);
      // Bumping the per-credential request version invalidates quota requests
      // started before this transaction, so their responses cannot overwrite
      // post-reset state.
      const cacheGeneration = captureQuotaCacheGeneration();
      const isCurrent = beginAccountQuotaRequest(quotaRequestVersionsRef.current, requestKey);

      const runResetTransaction = async () => {
        let fresh: CodexQuotaData;
        try {
          fresh = await CODEX_CONFIG.fetchQuota(row.raw, t, authFilesRequestScope);
        } catch (err: unknown) {
          endResetTransaction();
          if (!isCurrent()) return;
          const message = err instanceof Error ? err.message : t('common.unknown_error');
          const status =
            typeof err === 'object' && err !== null && 'status' in err
              ? Number((err as { status?: unknown }).status)
              : undefined;
          commitIfQuotaCacheCurrent(cacheGeneration, () => {
            setCodexQuota((prev) => ({
              ...prev,
              [storeKey]: buildQuotaFailureState(
                CODEX_CONFIG,
                message,
                Number.isFinite(status) ? status : undefined,
                row.raw,
                getScopedQuotaState(CODEX_CONFIG, prev, row.raw)
              ),
            }));
          });
          showNotification(
            t('codex_quota.reset_verify_failed', { name: displayName, message }),
            'error'
          );
          return;
        }
        if (!isCurrent()) {
          endResetTransaction();
          return;
        }
        if (
          fresh.rateLimitResetCreditsAvailableCount === null &&
          fresh.rateLimitResetCredits.length === 0 &&
          fresh.rateLimitResetCreditsError
        ) {
          endResetTransaction();
          showNotification(
            t('codex_quota.reset_verify_failed', {
              name: displayName,
              message: fresh.rateLimitResetCreditsError,
            }),
            'error'
          );
          return;
        }
        const verifiedCount =
          fresh.rateLimitResetCreditsAvailableCount ?? fresh.rateLimitResetCredits.length;
        const verifiedState = CODEX_CONFIG.buildSuccessState(fresh, row.raw);
        commitIfQuotaCacheCurrent(cacheGeneration, () => {
          setCodexQuota((prev) => ({ ...prev, [storeKey]: verifiedState }));
        });
        if (verifiedCount <= 0) {
          endResetTransaction();
          showNotification(t('codex_quota.reset_no_credits', { name: displayName }), 'info');
          return;
        }
        let confirmed = false;
        showConfirmation({
          title: t('codex_quota.reset_confirm_title'),
          message: t('codex_quota.reset_confirm_message', {
            name: displayName,
            count: verifiedCount,
          }),
          confirmText: t('codex_quota.reset_button', { count: verifiedCount }),
          cancelText: t('common.cancel'),
          variant: 'primary',
          onCancel: endResetTransaction,
          onConfirm: async () => {
            if (confirmed) return;
            confirmed = true;
            try {
              await consumeCodexRateLimitResetCredit(row.raw, t, authFilesRequestScope);
            } catch (err: unknown) {
              endResetTransaction();
              const message = err instanceof Error ? err.message : t('common.unknown_error');
              showNotification(
                t('codex_quota.reset_failed', { name: displayName, message }),
                'error'
              );
              return;
            }
            const postMutationIsCurrent = beginAccountQuotaRequest(
              quotaRequestVersionsRef.current,
              requestKey
            );
            // The reset mutation has happened; drop the pre-reset evidence so
            // the UI cannot keep offering it until the refreshed quota lands.
            commitIfQuotaCacheCurrent(cacheGeneration, () => {
              setCodexQuota((prev) => ({
                ...prev,
                [storeKey]: {
                  ...(getScopedQuotaState(CODEX_CONFIG, prev, row.raw) ?? verifiedState),
                  rateLimitResetCreditsAvailableCount: null,
                  rateLimitResetCredits: [],
                },
              }));
            });
            setQuotaSnapshotWindowsByRowKey((current) => {
              const windows = current.get(row.selectionKey);
              if (!windows) return current;
              const next = new Map(current);
              next.set(
                row.selectionKey,
                windows.map((window) => ({
                  ...window,
                  reset_credits_available: undefined,
                  reset_credits: undefined,
                }))
              );
              return next;
            });
            try {
              const data = await CODEX_CONFIG.fetchQuota(row.raw, t, authFilesRequestScope);
              endResetTransaction();
              if (postMutationIsCurrent()) {
                commitIfQuotaCacheCurrent(cacheGeneration, () => {
                  setCodexQuota((prev) => ({
                    ...prev,
                    [storeKey]: CODEX_CONFIG.buildSuccessState(data, row.raw),
                  }));
                });
              }
              showNotification(t('codex_quota.reset_success', { name: displayName }), 'success');
            } catch {
              endResetTransaction();
              showNotification(
                t('codex_quota.reset_partial_success', { name: displayName }),
                'warning'
              );
            }
          },
        });
      };
      void runResetTransaction();
    },
    [
      canResetCodexQuota,
      getDisplayAccount,
      setCodexQuota,
      showConfirmation,
      showNotification,
      t,
      authFilesRequestScope,
    ]
  );

  const handleBatchStatus = useCallback(
    async (enabled: boolean, targets = selectedRows) => {
      const patchTargets = targets
        .filter((row) => !row.runtimeOnly)
        .map((row) => getAuthFilePatchTarget(row.raw));
      if (patchTargets.length === 0) return;
      setStatusUpdating(true);
      try {
        await batchSetStatus(patchTargets, enabled);
        await loadFiles();
        deselectAll();
      } finally {
        setStatusUpdating(false);
      }
    },
    [batchSetStatus, deselectAll, loadFiles, selectedRows]
  );

  const patchPriorityRows = useCallback(
    async (targets: AccountRow[], priority: number) => {
      const patchTargets = targets
        .filter((row) => !row.runtimeOnly)
        .map((row) => getAuthFilePatchTarget(row.raw));
      await batchPatchFields(patchTargets, { priority });
    },
    [batchPatchFields]
  );

  const handleBatchPrioritySave = useCallback(async () => {
    const priority = parsePriorityValue(batchPriorityValue);
    if (priority === null) {
      showNotification(t('accounts.priority_invalid'), 'error');
      return;
    }
    await patchPriorityRows(selectedRows, priority);
    setBatchPriorityOpen(false);
    setBatchPriorityValue('');
    setIsSelectionMode(false);
  }, [batchPriorityValue, patchPriorityRows, selectedRows, showNotification, t]);

  const patchWebsocketsRows = useCallback(
    async (targets: AccountRow[], websockets: boolean) => {
      const patchTargets = targets
        .filter((row) => !row.runtimeOnly && row.provider === CODEX_CONFIG.type)
        .map((row) => getAuthFilePatchTarget(row.raw));
      if (patchTargets.length === 0) {
        showNotification(t('accounts.no_codex_accounts_selected'), 'info');
        return;
      }
      await batchPatchFields(patchTargets, { websockets });
    },
    [batchPatchFields, showNotification, t]
  );

  const copyTextWithNotification = useCallback(
    async (text: string) => {
      const copied = await copyToClipboard(text);
      showNotification(
        copied
          ? t('notification.link_copied', { defaultValue: 'Copied to clipboard' })
          : t('notification.copy_failed', { defaultValue: 'Copy failed' }),
        copied ? 'success' : 'error'
      );
    },
    [showNotification, t]
  );

  const handleCopyIdentityText = useCallback(
    async (event: ReactMouseEvent<HTMLButtonElement>, text: string, feedbackKey: string) => {
      event.stopPropagation();
      const value = text.trim();
      if (!value) return;

      const copied = await copyToClipboard(value);
      if (!copied) {
        showNotification(t('notification.copy_failed', { defaultValue: 'Copy failed' }), 'error');
        return;
      }

      setCopiedIdentityKey(feedbackKey);
      if (identityCopyTimerRef.current !== null) {
        clearTimeout(identityCopyTimerRef.current);
      }
      identityCopyTimerRef.current = setTimeout(() => {
        setCopiedIdentityKey((current) => (current === feedbackKey ? null : current));
        identityCopyTimerRef.current = null;
      }, 1800);
    },
    [showNotification, t]
  );

  const openOauthExcludedEditor = useCallback(
    (provider?: string) => {
      const providerValue = (provider || (providerFilter !== 'all' ? providerFilter : '')).trim();
      setOauthExcludedEditorProvider(providerValue);
    },
    [providerFilter]
  );

  const openOauthModelAliasEditor = useCallback(
    (provider?: string) => {
      const providerValue = (provider || (providerFilter !== 'all' ? providerFilter : '')).trim();
      setOauthModelAliasEditorProvider(providerValue);
    },
    [providerFilter]
  );

  const reloadOauthRules = useCallback(async () => {
    await Promise.all([loadOauthExcluded(), loadOauthModelAlias()]);
  }, [loadOauthExcluded, loadOauthModelAlias]);

  const selectedAccountSortIndex = ACCOUNT_SORT_FIELD_OPTIONS.findIndex(
    (option) => option.value === accountSort.key
  );
  const selectedAccountSortOption = getAccountSortFieldOption(accountSort.key);
  const selectedAccountSortLabel = t(selectedAccountSortOption.labelKey);
  const selectedStatusFilterLabel =
    statusFilter === 'all'
      ? t('accounts.status_all')
      : isAccountCodexStatusFilter(statusFilter)
        ? t(`auth_files.codex_status_filter_${statusFilter}`)
        : statusFilter === 'unconfirmed'
          ? t('accounts.metric_unconfirmed')
          : t(`accounts.status_${statusFilter}`);
  const selectedPlanFilterLabel =
    planFilter === 'all' ? t('accounts.plan_all') : getPlanOptionLabel(rows, planFilter, t);
  const selectedQuotaFilterLabel =
    quotaBandFilter === 'all' ? t('accounts.quota_all') : t(`accounts.quota_${quotaBandFilter}`);
  const selectedOperationalFilterLabel = t(`accounts.operational_${effectiveOperationalFilter}`);
  const activeMobileFilterCount = [
    statusFilter !== 'all',
    effectiveOperationalFilter !== 'all',
    planFilter !== 'all',
    quotaBandFilter !== 'all',
    accountSort.key !== 'default',
  ].filter(Boolean).length;
  const mobileFilterSummary =
    activeMobileFilterCount === 0
      ? t('accounts.mobile_filters_default')
      : [
          statusFilter !== 'all' ? selectedStatusFilterLabel : null,
          effectiveOperationalFilter !== 'all' ? selectedOperationalFilterLabel : null,
          planFilter !== 'all' ? selectedPlanFilterLabel : null,
          quotaBandFilter !== 'all' ? selectedQuotaFilterLabel : null,
          accountSort.key !== 'default' ? selectedAccountSortLabel : null,
        ]
          .filter(Boolean)
          .join(' · ');

  const openAccountSortDropdown = () => {
    setHighlightedAccountSortIndex(selectedAccountSortIndex >= 0 ? selectedAccountSortIndex : 0);
    setIsAccountSortDropdownOpen(true);
  };

  const toggleAccountSortDropdown = () => {
    if (isAccountSortDropdownOpen) {
      setIsAccountSortDropdownOpen(false);
      return;
    }
    openAccountSortDropdown();
  };

  const closeAccountSortDropdown = () => {
    setIsAccountSortDropdownOpen(false);
    accountSortTriggerRef.current?.focus();
  };

  const closeMobileFilters = () => {
    setIsMobileFiltersOpen(false);
    setIsAccountSortDropdownOpen(false);
  };

  const resetAccountFilters = () => {
    setStatusFilter('all');
    setOperationalFilter('all');
    setPlanFilter('all');
    setQuotaBandFilter('all');
    setAccountSort({ key: 'default', direction: 'desc' });
    setPage(1);
    setIsAccountSortDropdownOpen(false);
  };

  const moveAccountSortHighlight = (nextIndex: number) => {
    const normalizedIndex =
      (nextIndex + ACCOUNT_SORT_FIELD_OPTIONS.length) % ACCOUNT_SORT_FIELD_OPTIONS.length;
    setHighlightedAccountSortIndex(normalizedIndex);
  };

  const commitAccountSortField = (value: AccountSortFieldValue) => {
    setAccountSort((prev) => {
      if (value === 'default') {
        return { key: 'default', direction: 'desc' };
      }
      return {
        key: value,
        direction: prev.key === value ? prev.direction : ACCOUNT_SORT_DEFAULT_DIRECTIONS[value],
      };
    });
    setPage(1);
    setIsAccountSortDropdownOpen(false);
    accountSortTriggerRef.current?.focus();
  };

  const handleAccountSortDirectionToggle = () => {
    setAccountSort((prev) => {
      if (prev.key === 'default') return prev;
      return {
        key: prev.key,
        direction: prev.direction === 'asc' ? 'desc' : 'asc',
      };
    });
    setPage(1);
  };

  const handleAccountSortTriggerKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    switch (event.key) {
      case 'Enter':
      case ' ':
        event.preventDefault();
        toggleAccountSortDropdown();
        return;
      case 'ArrowDown':
        event.preventDefault();
        if (!isAccountSortDropdownOpen) {
          openAccountSortDropdown();
          return;
        }
        moveAccountSortHighlight(highlightedAccountSortIndex + 1);
        return;
      case 'ArrowUp':
        event.preventDefault();
        if (!isAccountSortDropdownOpen) {
          openAccountSortDropdown();
          return;
        }
        moveAccountSortHighlight(highlightedAccountSortIndex - 1);
        return;
      case 'Escape':
        if (!isAccountSortDropdownOpen) return;
        event.preventDefault();
        closeAccountSortDropdown();
        return;
      default:
        return;
    }
  };

  const handleAccountSortOptionKeyDown = (
    event: KeyboardEvent<HTMLButtonElement>,
    optionIndex: number
  ) => {
    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault();
        moveAccountSortHighlight(optionIndex + 1);
        return;
      case 'ArrowUp':
        event.preventDefault();
        moveAccountSortHighlight(optionIndex - 1);
        return;
      case 'Home':
        event.preventDefault();
        moveAccountSortHighlight(0);
        return;
      case 'End':
        event.preventDefault();
        moveAccountSortHighlight(ACCOUNT_SORT_FIELD_OPTIONS.length - 1);
        return;
      case 'Escape':
        event.preventDefault();
        closeAccountSortDropdown();
        return;
      default:
        return;
    }
  };

  const renderViewTabs = () => {
    const tabs: Array<SegmentedTabItem<AccountsView> & { badge?: number }> = [
      { id: 'accounts', label: t('accounts.tab_accounts') },
      {
        id: 'health',
        label: t('accounts.tab_health'),
        badge: metrics.needsInspectionAction,
      },
      { id: 'oauth', label: t('accounts.tab_oauth') },
    ];
    return (
      <SegmentedTabs
        items={tabs.map((tab) => ({
          id: tab.id,
          label: (
            <span className={styles.tabLabel}>
              <span>{tab.label}</span>
              {tab.badge ? <small className={styles.tabBadge}>{tab.badge}</small> : null}
            </span>
          ),
        }))}
        activeTab={activeView}
        onChange={changeActiveView}
        ariaLabel={t('accounts.tabs_label')}
        idBase="accounts-tab"
        className={styles.tabs}
      />
    );
  };

  const renderAccountSortControls = () => {
    const selectedField: AccountSortFieldValue = accountSort.key;
    const directionLabel =
      accountSort.direction === 'asc'
        ? t('accounts.sort_ascending')
        : t('accounts.sort_descending');

    return (
      <div className={styles.accountSortControls} ref={accountSortDropdownRef}>
        <button
          ref={accountSortTriggerRef}
          type="button"
          className={styles.accountSortTrigger}
          onClick={toggleAccountSortDropdown}
          onKeyDown={handleAccountSortTriggerKeyDown}
          title={`${t('accounts.sort_label')}: ${selectedAccountSortLabel}`}
          aria-label={`${t('accounts.sort_label')}: ${selectedAccountSortLabel}`}
          aria-haspopup="listbox"
          aria-expanded={isAccountSortDropdownOpen}
        >
          <span className={styles.accountSortLabel}>{selectedAccountSortLabel}</span>
        </button>
        <button
          type="button"
          className={styles.accountSortDirectionButton}
          onClick={handleAccountSortDirectionToggle}
          disabled={accountSort.key === 'default'}
          title={directionLabel}
          aria-label={directionLabel}
        >
          <span className={styles.accountSortDirectionIcon} aria-hidden="true">
            {accountSort.direction === 'asc' ? (
              <IconArrowUpNarrowWide size={14} />
            ) : (
              <IconArrowDownWideNarrow size={14} />
            )}
          </span>
        </button>
        {isAccountSortDropdownOpen ? (
          <div className={styles.accountSortDropdownList} role="listbox">
            {ACCOUNT_SORT_FIELD_OPTIONS.map((option, optionIndex) => {
              const isSelected = option.value === selectedField;
              const isHighlighted = optionIndex === highlightedAccountSortIndex;
              const optionClassName = [
                styles.accountSortDropdownItem,
                isSelected ? styles.accountSortDropdownItemSelected : '',
                isHighlighted ? styles.accountSortDropdownItemHighlighted : '',
              ]
                .filter(Boolean)
                .join(' ');
              return (
                <button
                  key={option.value}
                  ref={(node) => {
                    accountSortOptionRefs.current.set(option.value, node);
                  }}
                  type="button"
                  role="option"
                  aria-selected={isSelected}
                  className={optionClassName}
                  onClick={() => commitAccountSortField(option.value)}
                  onKeyDown={(event) => handleAccountSortOptionKeyDown(event, optionIndex)}
                  onMouseEnter={() => setHighlightedAccountSortIndex(optionIndex)}
                >
                  {t(option.labelKey)}
                </button>
              );
            })}
          </div>
        ) : null}
      </div>
    );
  };

  const renderAccountFilterFields = () => (
    <>
      <div className={styles.filterField}>
        <Select
          value={statusFilter}
          options={[
            { value: 'all', label: t('accounts.status_all') },
            { value: 'available', label: t('accounts.status_available') },
            { value: 'unconfirmed', label: t('accounts.metric_unconfirmed') },
            { value: 'low', label: t('accounts.status_low') },
            { value: 'exhausted', label: t('accounts.status_exhausted') },
            { value: 'disabled', label: t('accounts.status_disabled') },
            { value: 'problem', label: t('accounts.status_problem') },
            { value: 'inspection', label: t('accounts.status_inspection') },
            ...ACCOUNT_CODEX_STATUS_FILTERS.map((value) => ({
              value,
              label: t(`auth_files.codex_status_filter_${value}`),
            })),
          ]}
          onChange={(value) => setStatusFilter(value as AccountStatusFilter)}
          ariaLabel={t('accounts.status_filter')}
          triggerClassName={styles.toolbarSelectTrigger}
        />
      </div>
      <div className={styles.filterField}>
        <Select
          value={effectiveOperationalFilter}
          options={[
            { value: 'all', label: t('accounts.operational_all') },
            { value: 'reauth', label: t('accounts.operational_reauth') },
            ...(managerStorageAvailable
              ? [
                  { value: 'cooldown', label: t('accounts.operational_cooldown') },
                  { value: 'automation', label: t('accounts.operational_automation') },
                ]
              : []),
            { value: 'recovered', label: t('accounts.operational_recovered') },
          ]}
          onChange={(value) => setOperationalFilter(value as AccountOperationalFilter)}
          ariaLabel={t('accounts.operational_filter')}
          triggerClassName={styles.toolbarSelectTrigger}
        />
      </div>
      <div className={styles.filterField}>
        <Select
          value={planFilterValue}
          options={[{ value: 'all', label: t('accounts.plan_all') }, ...effectivePlanOptions]}
          onChange={setPlanFilter}
          ariaLabel={t('accounts.plan_filter')}
          triggerClassName={styles.toolbarSelectTrigger}
        />
      </div>
      <div className={styles.filterField}>
        <Select
          value={quotaBandFilter}
          options={[
            { value: 'all', label: t('accounts.quota_all') },
            { value: 'ge50', label: t('accounts.quota_ge50') },
            { value: 'between20and50', label: t('accounts.quota_between20and50') },
            { value: 'lt20', label: t('accounts.quota_lt20') },
            { value: 'spent', label: t('accounts.quota_spent') },
          ]}
          onChange={(value) => setQuotaBandFilter(value as AccountQuotaBand)}
          ariaLabel={t('accounts.quota_filter')}
          triggerClassName={styles.toolbarSelectTrigger}
        />
      </div>
    </>
  );

  const renderSelectionControls = () => (
    <div className={styles.selectionControls}>
      <Button
        variant="secondary"
        size="sm"
        onClick={() => selectAllVisible(pageAuthFiles)}
        disabled={selectablePageRows.length === 0}
      >
        {t('auth_files.batch_select_page')}
      </Button>
      <Button
        variant="secondary"
        size="sm"
        onClick={() => selectAllVisible(filteredAuthFiles)}
        disabled={selectableFilteredRows.length === 0}
      >
        {t('auth_files.batch_select_filtered')}
      </Button>
      <Button
        variant="secondary"
        size="sm"
        onClick={() => invertVisibleSelection(pageAuthFiles)}
        disabled={selectablePageRows.length === 0}
      >
        {t('auth_files.batch_invert_page')}
      </Button>
      <Button
        variant="secondary"
        size="sm"
        onClick={cancelSelectionMode}
        disabled={!isSelectionMode && selectionCount === 0}
      >
        <IconX size={15} />
        {t('auth_files.batch_deselect')}
      </Button>
    </div>
  );

  const renderToolbar = () => (
    <>
      <AccountProviderTabs
        rows={rows}
        value={providerFilter}
        onChange={setProviderFilter}
        resolvedTheme={resolvedTheme}
      />
      <section className={styles.toolbar}>
        <div className={styles.searchField}>
          <Input
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder={t('accounts.search_placeholder')}
            rightElement={<IconSearch size={16} />}
            aria-label={t('accounts.search_label')}
            className={styles.toolbarSearchInput}
          />
        </div>
        <div className={styles.mobileToolbarActions}>
          <Button
            variant="secondary"
            size="sm"
            className={styles.mobileFilterButton}
            onClick={() => {
              setIsMobileFiltersOpen(true);
              setIsAccountSortDropdownOpen(false);
            }}
            aria-label={t('accounts.mobile_filters_button')}
          >
            <IconSlidersHorizontal size={15} />
            {t('accounts.mobile_filters_button')}
            {activeMobileFilterCount > 0 ? (
              <span className={styles.mobileFilterCount}>{activeMobileFilterCount}</span>
            ) : null}
          </Button>
          <span className={styles.mobileFilterSummary} title={mobileFilterSummary}>
            {mobileFilterSummary}
          </span>
        </div>
        {renderAccountFilterFields()}
        {renderAccountSortControls()}
      </section>
    </>
  );

  const renderAccountDisplayToggle = () => (
    <Button
      variant="secondary"
      size="sm"
      className={[
        styles.accountDisplayButton,
        accountDisplayMode === 'full' ? styles.accountDisplayButtonActive : '',
      ]
        .filter(Boolean)
        .join(' ')}
      onClick={() => setAccountDisplayMode((mode) => (mode === 'masked' ? 'full' : 'masked'))}
      title={accountDisplayHint}
      aria-label={accountDisplayHint}
    >
      <AccountDisplayIcon size={15} />
      {t(
        accountDisplayMode === 'masked'
          ? 'accounts.account_display_masked'
          : 'accounts.account_display_full'
      )}
    </Button>
  );

  const renderMobileFilterPanel = () => {
    if (!isMobileFiltersOpen || typeof document === 'undefined') return null;

    return createPortal(
      <div className={styles.mobileFilterLayer}>
        <button
          type="button"
          className={styles.mobileFilterBackdrop}
          aria-label={t('common.close')}
          onClick={closeMobileFilters}
        />
        <section
          className={styles.mobileFilterPanel}
          role="dialog"
          aria-modal="true"
          aria-labelledby="accounts-mobile-filter-title"
        >
          <header className={styles.mobileFilterHeader}>
            <div>
              <h2 id="accounts-mobile-filter-title">{t('accounts.mobile_filters_title')}</h2>
              <span>{mobileFilterSummary}</span>
            </div>
            <Button
              variant="ghost"
              size="sm"
              iconOnly
              onClick={closeMobileFilters}
              aria-label={t('common.close')}
            >
              <IconX size={17} />
            </Button>
          </header>
          <div className={styles.mobileFilterBody}>
            <div className={styles.mobileFilterDisplayMode}>{renderAccountDisplayToggle()}</div>
            {renderAccountFilterFields()}
            <div className={styles.mobileSortField}>{renderAccountSortControls()}</div>
          </div>
          <footer className={styles.mobileFilterFooter}>
            <Button variant="secondary" size="sm" onClick={resetAccountFilters}>
              {t('common.reset')}
            </Button>
            <Button variant="primary" size="sm" onClick={closeMobileFilters}>
              {t('common.confirm')}
            </Button>
          </footer>
        </section>
      </div>,
      document.body
    );
  };

  const renderBatchBar = () => {
    const hasSelection = selectionCount > 0;
    const refreshTargets = hasSelection ? selectedRows : pageRows;
    const showSelectionControls = isSelectionMode || hasSelection;

    return (
      <section
        className={[
          styles.batchBar,
          hasSelection || isSelectionMode ? styles.batchBarActive : styles.batchBarIdle,
        ]
          .filter(Boolean)
          .join(' ')}
      >
        <div className={styles.batchSummary}>
          <span>
            {t('accounts.selected_count', {
              count: selectionCount,
            })}
          </span>
          <small>
            {isSelectionMode || hasSelection
              ? t('accounts.selection_mode_hint')
              : t('accounts.batch_hint')}
          </small>
        </div>
        <div className={styles.batchActions}>
          {!isSelectionMode && !hasSelection ? (
            <Button
              variant="secondary"
              size="sm"
              className={styles.selectionModeButton}
              onClick={() => setIsSelectionMode(true)}
              aria-pressed={isSelectionMode}
            >
              <IconCheck size={15} />
              {t('accounts.selection_mode_enter')}
            </Button>
          ) : null}
          {showSelectionControls ? renderSelectionControls() : null}
          <div className={styles.batchDisplayToggle}>{renderAccountDisplayToggle()}</div>
          {!hasSelection && !isSelectionMode ? (
            <Button
              variant="secondary"
              size="sm"
              onClick={() => refreshQuotaRows(refreshTargets)}
              disabled={disableControls || quotaRefreshing || refreshTargets.length === 0}
              loading={quotaRefreshing}
              title={t('accounts.refresh_quota')}
            >
              {!quotaRefreshing ? <IconRefreshCw size={15} /> : null}
              {t('accounts.refresh_quota')}
            </Button>
          ) : null}
        </div>
      </section>
    );
  };

  const renderFloatingBatchActions = () => {
    if (selectionCount === 0) return null;
    const selectedProviderSummary = Array.from(
      selectedRows.reduce((summary, row) => {
        summary.set(row.provider, (summary.get(row.provider) ?? 0) + 1);
        return summary;
      }, new Map<string, number>())
    )
      .sort((left, right) => right[1] - left[1])
      .map(([provider, count]) => `${getProviderLabel(provider, t)} × ${count}`)
      .join(', ');
    const deletePreviewNames = selectedFileNames.slice(0, 6);
    const moreItems: DropdownMenuItem[] = [
      {
        key: 'download',
        label: t('auth_files.batch_download'),
        icon: <IconDownload size={15} />,
        onClick: () => void batchDownload(selectedFileNames),
        disabled: disableControls || selectedFileNames.length === 0,
      },
      {
        key: 'websockets-enable',
        label: t('auth_files.batch_websockets_enable'),
        icon: <IconSettings size={15} />,
        onClick: () => void patchWebsocketsRows(selectedRows, true),
        disabled: disableControls || selectedCodexRows.length === 0 || batchFieldsUpdating,
      },
      {
        key: 'websockets-disable',
        label: t('auth_files.batch_websockets_disable'),
        icon: <IconSettings size={15} />,
        onClick: () => void patchWebsocketsRows(selectedRows, false),
        disabled: disableControls || selectedCodexRows.length === 0 || batchFieldsUpdating,
      },
      { key: 'batch-more-divider', type: 'divider' },
      {
        key: 'restore-default',
        label: t('accounts.restore_default_priority'),
        icon: <IconRefreshCw size={15} />,
        onClick: () => {
          void patchPriorityRows(selectedRows, 0).then(() => setIsSelectionMode(false));
        },
        disabled: disableControls || batchFieldsUpdating,
      },
      { key: 'danger-divider', type: 'divider' },
      {
        key: 'delete',
        label: t('common.delete'),
        icon: <IconTrash2 size={15} />,
        onClick: () => {
          if (selectedHasPartialSharedAuthFile) return;
          batchDelete(selectedTargetFiles, {
            title: t('auth_files.batch_delete_title'),
            confirmText: t('common.delete'),
            message: (
              <AccountsBatchDeletePreview
                summary={t('accounts.batch_delete_preview_summary', {
                  rows: selectionCount,
                  files: selectedFileNames.length,
                })}
                warning={t('accounts.batch_delete_preview_file_scope')}
                providers={
                  selectedProviderSummary
                    ? t('accounts.batch_delete_preview_providers', {
                        providers: selectedProviderSummary,
                      })
                    : undefined
                }
                fileNames={deletePreviewNames.map(getDisplayFileName)}
                moreLabel={
                  selectedFileNames.length > deletePreviewNames.length
                    ? t('accounts.batch_delete_preview_more', {
                        count: selectedFileNames.length - deletePreviewNames.length,
                      })
                    : undefined
                }
              />
            ),
          });
        },
        disabled:
          disableControls || selectedFileNames.length === 0 || selectedHasPartialSharedAuthFile,
        tone: 'danger',
      },
    ];

    const content = (
      <div className={styles.floatingBatchActionContainer}>
        <div className={styles.floatingBatchActionBar}>
          <div className={styles.floatingBatchActionLeft}>
            <span className={styles.batchSelectionText}>
              {t('accounts.selected_count', {
                count: selectionCount,
              })}
            </span>
          </div>
          <div className={styles.floatingBatchActionRight}>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => refreshQuotaRows(selectedRows)}
              disabled={disableControls || quotaRefreshing || selectedRows.length === 0}
              loading={quotaRefreshing}
              title={t('accounts.refresh_quota')}
            >
              {!quotaRefreshing ? <IconRefreshCw size={15} /> : null}
              {t('accounts.refresh_quota')}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => handleBatchStatus(true)}
              disabled={disableControls || statusUpdating}
              title={t('accounts.enable')}
            >
              <IconCheck size={15} />
              {t('accounts.enable')}
            </Button>
            <Button
              variant="danger"
              size="sm"
              onClick={() => handleBatchStatus(false)}
              disabled={disableControls || statusUpdating}
              title={t('accounts.disable')}
            >
              <IconX size={15} />
              {t('accounts.disable')}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              disabled={disableControls || selectedRows.length === 0 || batchFieldsUpdating}
              onClick={() => {
                setBatchPriorityValue('');
                setBatchPriorityOpen(true);
              }}
              title={t('accounts.set_priority')}
            >
              {t('accounts.set_priority')}
            </Button>
            <DropdownMenu
              items={moreItems}
              ariaLabel={t('accounts.batch_more')}
              triggerLabel={t('accounts.batch_more')}
              triggerIcon={<IconMoreVertical size={16} />}
              triggerClassName={styles.floatingBatchMore}
            />
          </div>
        </div>
      </div>
    );

    return typeof document === 'undefined' ? content : createPortal(content, document.body);
  };

  const renderRowActions = (row: AccountRow, needsReauth = false) => (
    <div className={styles.rowActions} onClick={(event) => event.stopPropagation()}>
      <div className={styles.accountQuickActionsGrid}>
        {needsReauth ? (
          <Button
            variant="secondary"
            size="sm"
            iconOnly
            className={`${styles.accountIconButton} ${styles.accountIconButtonRefresh}`}
            onClick={() => handleReauthAccount(row.raw)}
            disabled={disableControls || row.runtimeOnly}
            title={t('accounts.recommend_action_reauth')}
            aria-label={t('accounts.recommend_action_reauth')}
          >
            <IconShield size={15} />
          </Button>
        ) : null}
        <Button
          variant="secondary"
          size="sm"
          iconOnly
          className={`${styles.accountIconButton} ${styles.accountIconButtonRefresh}`}
          onClick={() => void refreshAccountQuota(row)}
          disabled={disableControls || quotaRefreshing || row.runtimeOnly}
          title={t('accounts.refresh_quota')}
          aria-label={t('accounts.refresh_quota')}
        >
          <IconRefreshCw size={15} />
        </Button>
        <Button
          variant="secondary"
          size="sm"
          iconOnly
          className={`${styles.accountIconButton} ${styles.accountIconButtonSettings}`}
          onClick={() => void openAccountDetail(row, 'config')}
          disabled={row.runtimeOnly}
          title={t('accounts.detail_tab_config')}
          aria-label={t('accounts.detail_tab_config')}
        >
          <IconSettings size={15} />
        </Button>
        <Button
          variant="secondary"
          size="sm"
          iconOnly
          className={`${styles.accountIconButton} ${styles.accountIconButtonModels}`}
          onClick={() => void openAccountDetail(row, 'models')}
          disabled={row.runtimeOnly && row.provider !== 'aistudio'}
          title={t('auth_files.models_button')}
          aria-label={t('auth_files.models_button')}
        >
          <IconModelCluster size={15} />
        </Button>
        <Button
          variant="secondary"
          size="sm"
          iconOnly
          className={`${styles.accountIconButton} ${styles.accountIconButtonDownload}`}
          onClick={() => void handleDownload(row.fileName)}
          disabled={row.runtimeOnly}
          title={t('auth_files.download_button')}
          aria-label={t('auth_files.download_button')}
        >
          <IconDownload size={15} />
        </Button>
        <Button
          variant="danger"
          size="sm"
          iconOnly
          className={`${styles.accountIconButton} ${styles.accountIconButtonDelete}`}
          onClick={() => void handleAccountDelete(row.raw)}
          disabled={disableControls || row.runtimeOnly || deleting === row.fileName}
          title={t('auth_files.delete_button')}
          aria-label={t('auth_files.delete_button')}
        >
          {deleting === row.fileName ? <LoadingSpinner size={14} /> : <IconTrash2 size={15} />}
        </Button>
      </div>
      <span className={styles.accountActionsDivider} aria-hidden="true" />
      <div className={styles.accountSideActions}>
        <div className={styles.accountStatusSwitch}>
          <ToggleSwitch
            checked={!row.disabled}
            onChange={(enabled) => void handleBatchStatus(enabled, [row])}
            disabled={disableControls || statusUpdating || row.runtimeOnly}
            ariaLabel={t('auth_files.status_toggle_label')}
          />
        </div>
        <Button
          variant="ghost"
          size="xs"
          className={styles.rowDetailButton}
          onClick={() => void openAccountDetail(row)}
          title={t('accounts.open_detail', { name: row.fileName })}
          aria-label={t('accounts.open_detail', { name: row.fileName })}
        >
          {t('accounts.open_detail_short')}
        </Button>
      </div>
    </div>
  );

  const renderPagination = () => (
    <div className={styles.accountsPagination}>
      <PaginationControls
        count={filteredRows.length}
        currentPage={currentPage}
        totalPages={totalPages}
        startItem={paginationStartItem}
        endItem={paginationEndItem}
        pageSize={pageSize}
        pageSizeOptions={PAGE_SIZE_OPTIONS.map((option) => Number(option.value))}
        onPageChange={setPage}
        onPageSizeChange={(nextPageSize) => {
          setPageSize(nextPageSize);
          setPage(1);
        }}
        t={t}
      />
    </div>
  );

  const renderAccountEmptyState = () => (
    <EmptyState
      title={t('accounts.empty_title')}
      description={t('accounts.empty_desc')}
      action={
        <Button variant="secondary" onClick={() => void refreshAccountsWorkspace()}>
          {t('common.refresh')}
        </Button>
      }
    />
  );

  const renderAccountCards = (rowsToRender = pageRows, paged = true) => (
    <section className={styles.tablePanel}>
      {paged ? renderBatchBar() : null}
      {rowsToRender.length > 0 ? (
        <div className={styles.accountCardList}>
          <div className={styles.accountCardHeader} data-account-list-header="true">
            <span>{t('accounts.list_header_credential')}</span>
            <span>{t('accounts.list_header_availability')}</span>
            <span>{t('accounts.list_header_recent_requests')}</span>
            <span>{t('accounts.list_header_historical_usage')}</span>
            <span>{t('accounts.list_header_quota')}</span>
            <span>{t('accounts.list_header_actions')}</span>
          </div>
          {rowsToRender.map((row) => {
            const recommendation = recommendationBySelectionKey.get(row.selectionKey) ?? null;
            const accountHistory = accountHistoryByRowKey.get(row.selectionKey) ?? null;
            const quotaWindows =
              quotaDisplayWindowsByRowKey.get(row.selectionKey) ?? buildQuotaDisplayWindows(row);
            const standardQuotaWindows = quotaWindows.filter(isStandardAccountQuotaListWindow);
            const antigravityQuotaMatrix = buildAntigravityQuotaMatrix(row, quotaWindows);
            const quotaLifecycleBarOverride = getAccountQuotaLifecycleBarOverride(row.quota.status);
            const displayQuotaWindows = antigravityQuotaMatrix
              ? []
              : selectAccountQuotaListWindows(row, quotaWindows, standardQuotaWindows);
            const usesFallbackQuotaPresentation =
              !antigravityQuotaMatrix &&
              standardQuotaWindows.length === 0 &&
              displayQuotaWindows.length > 0;
            const quotaCooldown = quotaCooldownsByRowKey.get(row.selectionKey)?.[0] ?? null;
            const codexStatus = codexStatusBySelectionKey.get(row.selectionKey) ?? null;
            const item = buildAccountListItem(row, {
              t,
              recommendation,
              quotaCooldown,
              codexStatus,
              quotaWindows,
              requestEvidence: requestEvidenceBySelectionKey.get(row.selectionKey),
            });
            const quotaEmptyLabel =
              quotaWindows.length > 0
                ? t('accounts.quota_details_only')
                : t('accounts.quota_source_none');
            const quotaWindowTitle =
              antigravityQuotaMatrix?.rows
                .flatMap((matrixRow) =>
                  matrixRow.cells.map(
                    (cell) =>
                      `${cell.displayLabel} ${matrixRow.label} ${formatPercent(cell.window.remainingPercent)}`
                  )
                )
                .join(' · ') ||
              displayQuotaWindows
                .map((window) => {
                  const visibleScopeLabel = usesFallbackQuotaPresentation
                    ? getAccountQuotaFallbackVisibleScopeLabel(row, window)
                    : null;
                  const label = visibleScopeLabel
                    ? `${visibleScopeLabel} · ${window.label}`
                    : window.groupLabel
                      ? `${window.groupLabel} ${window.label}`
                      : window.label;
                  return `${label}: ${formatPercent(window.remainingPercent)}`;
                })
                .join('\n') ||
              quotaEmptyLabel;
            const healthTitle = t(
              item.health.tooltipKey,
              formatQuotaResetTooltipParams(
                item.health.tooltipParams,
                item.health.resetAtMs,
                i18n.language,
                item.health.cooldown?.recoverAtMs
              )
            );
            const accountHistoryError = accountHistoryErrorsByRowKey.get(row.selectionKey) ?? '';
            const accountHistoryMatched = accountHistory?.matched === true;
            const accountHistoryTitle = getAccountHistoryTitle(
              t,
              accountHistory,
              accountHistoryLoading,
              accountHistoryError,
              i18n.language
            );
            const accountHistoryFootnote = accountHistoryError
              ? row.usage.success + row.usage.failure > 0
                ? t('accounts.history_recent_fallback')
                : t('accounts.history_unavailable')
              : accountHistoryLoading && !accountHistory
                ? t('accounts.history_loading')
                : accountHistory?.sync_status === 'pending'
                  ? t('accounts.history_syncing')
                  : null;
            const recentRequestCount = row.usage.success + row.usage.failure;
            const accountHistoryRequestExactValue = accountHistoryMatched
              ? formatHistoryNumber(accountHistory.total_requests, i18n.language)
              : recentRequestCount > 0
                ? formatHistoryNumber(recentRequestCount, i18n.language)
                : '-';
            const accountHistoryTokenExactValue = accountHistoryMatched
              ? formatHistoryNumber(accountHistory.total_tokens, i18n.language)
              : '-';
            const accountHistoryCostExactValue = accountHistoryMatched
              ? formatUsd(accountHistory.total_cost)
              : '-';
            const accountHistorySuccessExactValue = accountHistoryMatched
              ? formatHistorySuccessRate(accountHistory.success_rate, 2)
              : row.usage.successRate !== null
                ? formatPercent(row.usage.successRate, 2)
                : '-';
            const accountHistoryRequestValue = accountHistoryMatched
              ? formatCompactNumber(accountHistory.total_requests)
              : recentRequestCount > 0
                ? formatCompactNumber(recentRequestCount)
                : '-';
            const accountHistoryTokenValue = accountHistoryMatched
              ? formatCompactNumber(accountHistory.total_tokens)
              : '-';
            const accountHistoryCostValue = accountHistoryMatched
              ? formatCompactUsd(accountHistory.total_cost)
              : '-';
            const accountHistorySuccessValue = accountHistoryMatched
              ? formatHistorySuccessRate(accountHistory.success_rate)
              : row.usage.successRate !== null
                ? formatPercent(row.usage.successRate, 1)
                : '-';
            return (
              <article
                key={row.selectionKey}
                data-account-card={row.selectionKey}
                aria-selected={selectedFiles.has(row.selectionKey)}
                className={[
                  styles.accountCard,
                  selectedRowKey === row.selectionKey ? styles.accountCardSelected : '',
                  selectedFiles.has(row.selectionKey) ? styles.accountCardBulkSelected : '',
                  isSelectionMode ? styles.accountCardSelectionMode : '',
                ]
                  .filter(Boolean)
                  .join(' ')}
                onClick={isSelectionMode ? () => handleAccountCardClick(row) : undefined}
              >
                <div className={styles.accountCardIdentity}>
                  <div className={styles.accountIdentityBadgeRow}>
                    <span className={styles.providerPill}>
                      {getProviderLabel(item.identity.provider, t)}
                    </span>
                    {item.identity.planPresentation ? (
                      <span
                        className={styles.accountMetaPill}
                        title={item.identity.planPresentation.fullLabel}
                      >
                        {item.identity.planPresentation.shortLabel}
                      </span>
                    ) : null}
                  </div>
                  <div className={styles.accountIdentityCopyLine}>
                    <button
                      type="button"
                      className={styles.accountIdentityCopyTarget}
                      title={row.accountLabel}
                      aria-label={`${t('common.copy')} ${row.accountLabel}`}
                      onClick={(event) =>
                        void handleCopyIdentityText(
                          event,
                          row.accountLabel,
                          `${row.selectionKey}:account`
                        )
                      }
                    >
                      <strong className={styles.accountIdentityTitle}>
                        {getDisplayAccount(row)}
                      </strong>
                    </button>
                    {copiedIdentityKey === `${row.selectionKey}:account` ? (
                      <span className={styles.accountIdentityCopyHint}>
                        {t('accounts.copy_feedback_copied')}
                      </span>
                    ) : null}
                  </div>
                  <div className={styles.accountIdentityCopyLine}>
                    <button
                      type="button"
                      className={styles.accountIdentityCopyTarget}
                      title={row.fileName}
                      aria-label={`${t('common.copy')} ${row.fileName}`}
                      onClick={(event) =>
                        void handleCopyIdentityText(event, row.fileName, `${row.selectionKey}:file`)
                      }
                    >
                      <span className={styles.accountCardFile}>
                        {getDisplayFileName(row.fileName)}
                      </span>
                    </button>
                    {copiedIdentityKey === `${row.selectionKey}:file` ? (
                      <span className={styles.accountIdentityCopyHint}>
                        {t('accounts.copy_feedback_copied')}
                      </span>
                    ) : null}
                  </div>
                </div>

                <div className={styles.accountCardHealth}>
                  <div className={styles.accountCardLine}>
                    <span
                      className={`${styles.badge} ${getHealthStatusClass(item.health.status)}`}
                      title={healthTitle}
                    >
                      {t(item.health.labelKey)}
                    </span>
                  </div>
                  <div className={styles.accountHealthMetaRow}>
                    <span
                      className={
                        item.identity.priorityIsNegative
                          ? styles.accountPriorityMetaDanger
                          : styles.accountPriorityMeta
                      }
                      title={t('accounts.col_priority')}
                    >
                      {t('accounts.col_priority')} {item.identity.priority}
                    </span>
                  </div>
                </div>

                <div className={styles.accountCardLatestRequest}>
                  <AccountLatestRequest
                    latestRequest={accountHistory?.latest_request}
                    recentRequests={accountHistory?.recent_requests}
                    loading={accountHistoryLoading && !accountHistory}
                    unavailable={Boolean(accountHistoryError)}
                    locale={i18n.language}
                    onCopy={copyTextWithNotification}
                  />
                </div>

                {renderAccountDetailTrigger({
                  isSelectionMode,
                  className: styles.accountCardEvidence,
                  title: accountHistoryTitle,
                  ariaLabel: `${t('accounts.list_header_historical_usage')}: ${accountHistoryTitle}. ${t(
                    'accounts.open_detail',
                    { name: row.fileName }
                  )}: ${t('accounts.detail_tab_quota')}`,
                  kind: 'history',
                  onOpen: () => void openAccountDetail(row, 'quota'),
                  children: (
                    <>
                      <span className={styles.accountHistoryGrid}>
                        <span
                          className={`${styles.accountHistoryMetric} ${styles.accountHistoryMetricRequests}`}
                          aria-label={`${t('accounts.history_requests')}: ${accountHistoryRequestExactValue}`}
                        >
                          <span className={styles.accountHistoryIcon}>
                            <IconSend size={13} />
                          </span>
                          <strong>{accountHistoryRequestValue}</strong>
                        </span>
                        <span
                          className={`${styles.accountHistoryMetric} ${styles.accountHistoryMetricTokens}`}
                          aria-label={`${t('accounts.history_tokens')}: ${accountHistoryTokenExactValue}`}
                        >
                          <span className={styles.accountHistoryIcon}>
                            <IconBinary size={13} />
                          </span>
                          <strong>{accountHistoryTokenValue}</strong>
                        </span>
                        <span
                          className={`${styles.accountHistoryMetric} ${styles.accountHistoryMetricCost}`}
                          aria-label={`${t('accounts.history_cost')}: ${accountHistoryCostExactValue}`}
                        >
                          <span className={styles.accountHistoryIcon}>
                            <IconDollarSign size={13} />
                          </span>
                          <strong>{accountHistoryCostValue}</strong>
                        </span>
                        <span
                          className={`${styles.accountHistoryMetric} ${styles.accountHistoryMetricSuccess}`}
                          aria-label={`${t('accounts.history_success')}: ${accountHistorySuccessExactValue}`}
                        >
                          <span className={styles.accountHistoryIcon}>
                            <IconCheck size={13} />
                          </span>
                          <strong>{accountHistorySuccessValue}</strong>
                        </span>
                      </span>
                      {accountHistoryFootnote ? (
                        <span className={styles.accountHistoryFootnote}>
                          {accountHistoryFootnote}
                        </span>
                      ) : null}
                    </>
                  ),
                })}

                {renderAccountDetailTrigger({
                  isSelectionMode,
                  className: styles.accountCardBusiness,
                  title: quotaWindowTitle,
                  ariaLabel: `${t('accounts.list_header_quota')}: ${quotaWindowTitle}. ${t(
                    'accounts.open_detail',
                    { name: row.fileName }
                  )}: ${t('accounts.detail_tab_quota')}`,
                  kind: 'quota',
                  onOpen: () => void openAccountDetail(row, 'quota'),
                  children: (
                    <span className={styles.quotaWindowGrid} title={quotaWindowTitle}>
                      {antigravityQuotaMatrix ? (
                        <AccountQuotaMatrix
                          accountKey={row.selectionKey}
                          matrix={antigravityQuotaMatrix}
                          lifecycleBarOverride={quotaLifecycleBarOverride}
                        />
                      ) : displayQuotaWindows.length > 0 ? (
                        displayQuotaWindows.map((window) => {
                          const windowRemaining = window.remainingPercent;
                          const windowWidth = Math.max(0, Math.min(100, windowRemaining ?? 0));
                          const resetLabel =
                            window.resetLabel && window.resetLabel !== '-' ? window.resetLabel : '';
                          const resetDisplayLabel = formatQuotaResetDisplay(
                            window.resetAtMs,
                            resetLabel,
                            i18n.language
                          );
                          const shortLabel = getQuotaWindowShortLabel(window);
                          const visibleScopeLabel = usesFallbackQuotaPresentation
                            ? getAccountQuotaFallbackVisibleScopeLabel(row, window)
                            : null;
                          const quotaWindowLabel = visibleScopeLabel
                            ? `${visibleScopeLabel} · ${window.label}`
                            : window.label;
                          const barClass = usesFallbackQuotaPresentation
                            ? getFallbackWindowBarClass(quotaLifecycleBarOverride, windowRemaining)
                            : getRemainingBarClass(row);
                          return (
                            <span
                              key={window.key}
                              className={styles.quotaWindowCard}
                              data-account-quota-window={window.key}
                              title={`${quotaWindowLabel}: ${formatPercent(windowRemaining)}`}
                            >
                              <span
                                className={`${styles.quotaWindowPrimaryLine} ${
                                  visibleScopeLabel ? styles.quotaWindowPrimaryLineScoped : ''
                                }`}
                              >
                                <span
                                  className={`${styles.quotaWindowSummary} ${
                                    visibleScopeLabel ? styles.quotaWindowSummaryScoped : ''
                                  }`}
                                  title={quotaWindowLabel}
                                >
                                  {visibleScopeLabel ? (
                                    <span className={styles.quotaWindowScopeLabel}>
                                      {visibleScopeLabel} ·
                                    </span>
                                  ) : null}
                                  <span>{shortLabel}</span>
                                </span>
                                <span className={styles.quotaTrack} aria-hidden="true">
                                  <span
                                    className={`${styles.quotaBar} ${barClass}`}
                                    style={{ width: `${windowWidth}%` }}
                                  />
                                </span>
                                <strong className={styles.quotaWindowPercent}>
                                  {windowRemaining !== null ? formatPercent(windowRemaining) : '-'}
                                </strong>
                                <span
                                  className={styles.quotaResetMeta}
                                  title={
                                    resetDisplayLabel !== '-'
                                      ? `${t('accounts.col_reset')}: ${resetDisplayLabel}`
                                      : ''
                                  }
                                >
                                  {resetDisplayLabel}
                                </span>
                              </span>
                            </span>
                          );
                        })
                      ) : (
                        <span className={styles.quotaEmptyState} data-account-quota-empty="true">
                          {quotaEmptyLabel}
                        </span>
                      )}
                    </span>
                  ),
                })}

                <div className={styles.accountCardRecommendation}>
                  {renderRowActions(row, item.health.status === 'reauth')}
                </div>
              </article>
            );
          })}
        </div>
      ) : (
        renderAccountEmptyState()
      )}
      {paged ? renderPagination() : null}
    </section>
  );

  const renderDetailDrawer = () => {
    if (!selectedRow) {
      return (
        <Drawer
          open={false}
          onClose={() => setSelectedRowKey(null)}
          width="clamp(540px, 45vw, 720px)"
          className={styles.accountDetailDrawer}
        />
      );
    }

    const providerIcon = getAuthFileIcon(selectedRow.provider, resolvedTheme);

    const detailTabs: Array<{ id: DetailTab; label: string }> = [
      { id: 'overview', label: t('accounts.detail_tab_overview') },
      { id: 'quota', label: t('accounts.detail_tab_quota') },
      { id: 'config', label: t('accounts.detail_tab_config') },
      { id: 'models', label: t('accounts.detail_tab_models') },
      { id: 'diagnostics', label: t('accounts.detail_tab_diagnostics') },
    ];
    const valueRow =
      usageRows.find((row) => row.row?.selectionKey === selectedRow.selectionKey) ??
      usageRows.find((row) => !row.row && row.fileName === selectedRow.fileName);
    const selectedQuotaWindows = (
      effectiveQuotaWindowDefinitionsByRowKey.get(selectedRow.selectionKey) ??
      buildAccountQuotaWindowDefinitions(buildQuotaDisplayWindows(selectedRow))
    ).map((definition) => ({
      ...definition.display,
      providerWindowId: definition.providerWindowId,
      resetAtMs: definition.cycleEndMs,
      resetAccuracy:
        definition.boundaryAccuracy === 'exact'
          ? ('exact' as const)
          : definition.boundaryAccuracy === 'derived' || definition.boundaryAccuracy === 'estimated'
            ? ('estimated' as const)
            : ('unknown' as const),
      fromMs: definition.cycleStartMs,
      toMs: definition.cycleEndMs,
      observationSource: definition.observationSource,
      observedAtMs: definition.observedAtMs,
      windowMode: definition.windowMode,
      cycleStartMs: definition.cycleStartMs,
      cycleEndMs: definition.cycleEndMs,
      limitWindowSeconds: definition.durationSeconds,
      modelScope: definition.modelScope,
      boundaryAccuracy: definition.boundaryAccuracy,
      stale: definition.stale,
      logicalWindowId: definition.logicalWindowId,
      activationGeneration: definition.activationGeneration,
      availability: definition.availability,
      relationshipKind: definition.relationshipKind,
      containerProviderWindowId: definition.containerProviderWindowId,
      firstSeenAtMs: definition.firstSeenAtMs,
      lastSeenAtMs: definition.lastSeenAtMs,
      missingSinceMs: definition.missingSinceMs,
      deactivatedAtMs: definition.deactivatedAtMs,
      currentCycle: definition.currentCycle,
      previousCycle: definition.previousCycle,
    }));
    const selectedQuotaCooldown = quotaCooldownsByRowKey.get(selectedRow.selectionKey)?.[0] ?? null;
    const selectedCodexQuota =
      selectedRow.provider === CODEX_CONFIG.type
        ? getDisplayCodexResetEvidenceQuota(selectedRow)
        : undefined;
    const resettingSelectedQuota =
      selectedRow.provider === CODEX_CONFIG.type &&
      resettingQuotaKeys.has(
        `${CODEX_CONFIG.type}:${CODEX_CONFIG.getStoreKey?.(selectedRow.raw) ?? selectedRow.fileName}`
      );
    const selectedCodexStatus = codexStatusBySelectionKey.get(selectedRow.selectionKey) ?? null;
    const hasMatchingDetailEvents = detailEventsRowKey === selectedRow.selectionKey;
    const rowEvents = hasMatchingDetailEvents ? detailEvents : [];
    const rowEventsSummary = hasMatchingDetailEvents ? detailEventsSummary : null;
    const rowEventsRecentFailure = hasMatchingDetailEvents ? detailEventsRecentFailure : null;
    const rowEventsTotalCount = hasMatchingDetailEvents ? detailEventsTotalCount : 0;
    const detailView = buildAccountDetailViewModel(selectedRow, {
      t,
      recommendation: recommendationBySelectionKey.get(selectedRow.selectionKey) ?? null,
      quotaCooldown: selectedQuotaCooldown,
      codexStatus: selectedCodexStatus,
      quotaWindows: selectedQuotaWindows,
      windowUsageByKey: matchingAccountWindowUsageByKey,
      actionCandidates: accountActionCandidates,
      matchedActionCandidates: actionCandidatesByRowKey.get(selectedRow.selectionKey) ?? [],
      history: accountHistoryByRowKey.get(selectedRow.selectionKey) ?? null,
      valueRow,
      codexQuota: selectedCodexQuota,
      xaiQuota:
        selectedRow.provider === XAI_CONFIG.type
          ? getCredentialScopedQuotaState(xaiQuota, selectedRow.raw)
          : undefined,
      diagnosticsSummary: rowEventsSummary,
      diagnosticsRecentFailure: rowEventsRecentFailure,
      diagnosticsEvents: rowEvents,
      diagnosticsTotalCount: rowEventsTotalCount,
    });
    const eventsUnavailable =
      !featureAvailability.requestMonitoringAvailable ||
      !featureAvailability.managerServiceBase ||
      !managementKey;
    const modelsTargetMatches = modelsSelectionKey === selectedRow.selectionKey;

    const renderActiveDetail = () => {
      if (detailTab === 'quota') {
        return (
          <AccountQuotaTab
            detailView={detailView}
            windowUsageError={accountWindowUsageError}
            historyAvailable={requestHistoryAvailable}
            historyRefreshing={historyRefreshing}
            onRefreshHistory={() => void refreshAccountHistory(selectedRow)}
            onResetQuota={() => resetCodexQuotaForRow(selectedRow)}
            resetQuotaDisabled={
              !canResetCodexQuota(selectedRow) || resettingSelectedQuota || configurationSaving
            }
          />
        );
      }
      if (detailTab === 'config') {
        return (
          <AccountConfigurationTab
            row={selectedRow}
            disableControls={disableControls}
            editor={configurationEditor}
            onCopyText={copyTextWithNotification}
          />
        );
      }
      if (detailTab === 'models') {
        return (
          <AccountModelsTab
            key={`${selectedRow.selectionKey}:models`}
            row={selectedRow}
            disableControls={disableControls}
            fileName={getDisplayFileName(
              modelsTargetMatches ? modelsFileName || selectedRow.fileName : selectedRow.fileName
            )}
            fileType={
              modelsTargetMatches ? modelsFileType || selectedRow.provider : selectedRow.provider
            }
            loading={modelsTargetMatches ? modelsLoading : true}
            refreshing={modelsTargetMatches ? modelsRefreshing : false}
            error={modelsTargetMatches ? modelsError : null}
            models={modelsTargetMatches ? modelsList : []}
            modelDefinitions={modelsTargetMatches ? modelDefinitions : []}
            modelDefinitionsLoading={modelsTargetMatches ? modelDefinitionsLoading : true}
            modelDefinitionsError={modelsTargetMatches ? modelDefinitionsError : null}
            globalExcluded={oauthState.excluded}
            globalExcludedState={oauthState.excludedError}
            aliases={oauthState.modelAlias}
            editor={configurationEditor}
            onRefresh={() => void handleAccountModelsRefresh()}
            onManageGlobalRules={() => void changeActiveView('oauth')}
            onOpenAdvancedRules={() => void openAccountDetail(selectedRow, 'config')}
            onCopyText={copyTextWithNotification}
          />
        );
      }
      if (detailTab === 'diagnostics') {
        return (
          <AccountDiagnosticsTab
            row={selectedRow}
            detailView={detailView}
            inspectionLoading={inspectionLoading}
            candidatesLoading={accountActionCandidatesLoading}
            candidatesError={accountActionCandidatesError}
            events={rowEvents}
            eventsTotalCount={rowEventsTotalCount}
            eventsHasMore={detailEventsHasMore}
            eventsLoading={detailEventsLoading}
            eventsRefreshing={detailEventsRefreshing}
            eventsAppending={detailEventsAppending}
            eventsError={detailEventsError}
            eventsUnavailable={eventsUnavailable}
            nextBeforeMs={detailEventsNextBeforeMs}
            nextBeforeId={detailEventsNextBeforeId}
            onRefreshEvents={() => void refreshDetailEvents(selectedRow)}
            onLoadMoreEvents={(beforeMs, beforeId) =>
              void loadDetailEvents(selectedRow, {
                append: true,
                beforeMs,
                beforeId,
              })
            }
          />
        );
      }
      return (
        <AccountOverviewTab
          detailView={detailView}
          getHealthStatusClass={getHealthStatusClass}
          onSelectTab={(tab) => void openAccountDetail(selectedRow, tab)}
        />
      );
    };
    const selectedCredentialRefreshing =
      credentialRefreshing[getAuthFileSelectionKey(selectedRow.raw)] === true;
    const drawerMoreItems: DropdownMenuItem[] = [
      {
        key: 'models',
        label: t('auth_files.models_button'),
        icon: <IconModelCluster size={15} />,
        onClick: () => {
          void openAccountDetail(selectedRow, 'models');
        },
        disabled: selectedRow.runtimeOnly && selectedRow.provider !== 'aistudio',
      },
      ...(selectedRow.provider === CODEX_CONFIG.type && !selectedRow.runtimeOnly
        ? [
            {
              key: 'refresh-credential',
              label: t('auth_files.credential_refresh_button'),
              icon: <IconRefreshCw size={15} />,
              onClick: () => {
                void handleCredentialRefresh(selectedRow.raw);
              },
              disabled: disableControls || configurationSaving || selectedCredentialRefreshing,
            } satisfies DropdownMenuItem,
          ]
        : []),
      {
        key: 'download',
        label: t('auth_files.download_button'),
        icon: <IconDownload size={15} />,
        onClick: () => void handleDownload(selectedRow.fileName),
        disabled: selectedRow.runtimeOnly,
      },
      ...(canResetCodexQuota(selectedRow)
        ? [
            {
              key: 'reset-codex-quota',
              label: t('codex_quota.reset_action_button'),
              icon: <IconRefreshCw size={15} />,
              onClick: () => resetCodexQuotaForRow(selectedRow),
              disabled: configurationSaving || resettingSelectedQuota,
            } satisfies DropdownMenuItem,
          ]
        : []),
      { key: 'drawer-danger-divider', type: 'divider' },
      {
        key: 'delete',
        label: t('auth_files.delete_button'),
        icon: <IconTrash2 size={15} />,
        onClick: () => void handleAccountDelete(selectedRow.raw),
        disabled:
          disableControls ||
          configurationSaving ||
          selectedRow.runtimeOnly ||
          deleting === selectedRow.fileName,
        tone: 'danger',
      },
    ];

    return (
      <Drawer
        key={selectedRow.selectionKey}
        open
        onClose={closeAccountDetail}
        onBeforeClose={confirmConfigurationDiscard}
        width="clamp(540px, 45vw, 720px)"
        className={styles.accountDetailDrawer}
        bodyRef={detailDrawerBodyRef}
        title={
          <div className={styles.drawerTitleIdentity}>
            <span
              className={styles.drawerProviderIcon}
              data-account-provider-icon={selectedRow.provider}
              aria-hidden="true"
            >
              {providerIcon ? <img src={providerIcon} alt="" /> : <IconKey size={24} />}
            </span>
            <div className={styles.drawerTitleStack}>
              <strong className={styles.drawerTitlePrimary} title={selectedRow.accountLabel}>
                {getDisplayAccount(selectedRow)}
              </strong>
              <span className={styles.drawerTitleMeta}>
                {getProviderLabel(selectedRow.provider, t)} ·{' '}
                <span title={detailView.identity.planPresentation?.fullLabel}>
                  {detailView.identity.planPresentation?.shortLabel ?? '-'}
                </span>{' '}
                ·{' '}
                <button
                  type="button"
                  className={styles.drawerFileNameCopy}
                  onClick={() => copyTextWithNotification(selectedRow.fileName)}
                  title={t('common.copy', { defaultValue: '点击复制' })}
                  aria-label={`${t('common.copy', { defaultValue: '点击复制' })} ${getDisplayFileName(selectedRow.fileName)}`}
                >
                  {getDisplayFileName(selectedRow.fileName)}
                  <IconCopy size={12} />
                </button>
              </span>
            </div>
          </div>
        }
        footer={
          <div className={styles.drawerActions}>
            {detailView.health.status === 'reauth' ? (
              <Button
                variant="primary"
                onClick={() => handleReauthAccount(selectedRow.raw)}
                disabled={disableControls || configurationSaving || selectedRow.runtimeOnly}
              >
                <IconShield size={16} />
                {t('accounts.recommend_action_reauth')}
              </Button>
            ) : null}
            <Button
              variant="secondary"
              onClick={() => void refreshAccountQuota(selectedRow)}
              loading={quotaRefreshing}
              disabled={disableControls || selectedRow.runtimeOnly}
            >
              {!quotaRefreshing ? <IconRefreshCw size={16} /> : null}
              {t('accounts.refresh_quota')}
            </Button>
            <Button
              variant={selectedRow.disabled ? 'secondary' : 'danger'}
              onClick={() => handleBatchStatus(selectedRow.disabled, [selectedRow])}
              disabled={
                disableControls || configurationSaving || statusUpdating || selectedRow.runtimeOnly
              }
            >
              {selectedRow.disabled ? t('accounts.enable') : t('accounts.disable')}
            </Button>
            <DropdownMenu
              items={drawerMoreItems}
              ariaLabel={t('accounts.drawer_more_actions')}
              triggerTitle={t('accounts.drawer_more_actions')}
              triggerLabel={t('accounts.batch_more')}
              triggerIcon={<IconMoreVertical size={16} />}
              triggerClassName={styles.drawerMoreActions}
            />
          </div>
        }
      >
        <div className={styles.drawerBodyShell} data-detail-tab={detailTab}>
          {selectedRow.disabled ? (
            <div className={styles.drawerDisabledNotice} role="status">
              <span>
                {t('accounts.detail_disabled_notice_title', { defaultValue: '账号已禁用' })}
              </span>
              <p>
                {t('accounts.detail_disabled_notice_desc', {
                  defaultValue:
                    '此账号当前不接收业务请求，但仍可刷新额度和维护配置。点击底部“启用”按钮可恢复请求路由。',
                })}
              </p>
            </div>
          ) : null}
          <div
            className={styles.drawerTabs}
            role="tablist"
            aria-label={t('accounts.detail_tablist_label')}
          >
            {detailTabs.map((tab) => (
              <button
                key={tab.id}
                type="button"
                role="tab"
                id={`accounts-detail-tab-${tab.id}`}
                aria-selected={detailTab === tab.id}
                aria-controls="accounts-detail-tab-panel"
                className={detailTab === tab.id ? styles.drawerTabActive : ''}
                onClick={() => void openAccountDetail(selectedRow, tab.id)}
              >
                {tab.label}
              </button>
            ))}
          </div>
          <div
            id="accounts-detail-tab-panel"
            className={styles.drawerTabPanel}
            role="tabpanel"
            aria-labelledby={`accounts-detail-tab-${detailTab}`}
          >
            {renderActiveDetail()}
          </div>
        </div>
      </Drawer>
    );
  };

  const renderAccountsOverview = () => (
    <>
      <AccountMetricsGrid metrics={metrics} />
      {error ? <div className={styles.errorBox}>{error}</div> : null}
      {loading ? (
        <div className={styles.loadingPanel}>
          <LoadingSpinner />
        </div>
      ) : (
        renderAccountCards()
      )}
      {renderDetailDrawer()}
    </>
  );

  const renderHealthView = () => (
    <CredentialHealthInspectionWorkspace
      mode={healthMode}
      onModeChange={changeHealthMode}
      onSnapshotChange={handleInspectionSnapshotChange}
      onCredentialsChanged={handleInspectionCredentialsChanged}
      onCodexReauthStart={handleInspectionCodexReauthStart}
      onOpenCredential={handleOpenInspectionCredential}
    />
  );

  const renderOAuthPreviewRow = (row: (typeof oauthPreviewRows)[number]) => {
    const catalogLabel = row.catalogModels.length
      ? row.catalogModels
          .map((model) => (model.displayName ? `${model.id} (${model.displayName})` : model.id))
          .join(' · ')
      : t('accounts.oauth_preview_catalog_hidden');

    return (
      <div
        key={row.provider}
        className={styles.previewRow}
        data-oauth-preview-provider={row.provider}
        data-oauth-preview-status={row.effectiveStatus}
      >
        <div className={styles.previewRowHeader}>
          <strong>{getProviderLabel(row.provider, t)}</strong>
          <span className={styles.previewStatus}>
            {t(`accounts.oauth_preview_status_${row.effectiveStatus}`)}
          </span>
        </div>
        <dl className={styles.previewDetails}>
          <div>
            <dt>{t('accounts.oauth_preview_route_label')}</dt>
            <dd>
              {row.inputModel || '-'} → {row.upstreamModel || '-'}
            </dd>
          </div>
          <div>
            <dt>{t('accounts.oauth_preview_catalog_label')}</dt>
            <dd>{catalogLabel}</dd>
          </div>
          <div>
            <dt>{t('accounts.oauth_preview_response_label')}</dt>
            <dd>
              {row.responseModel || '-'} ·{' '}
              {t(
                row.forceMapping
                  ? 'accounts.oauth_preview_response_forced'
                  : 'accounts.oauth_preview_response_passthrough'
              )}
            </dd>
          </div>
        </dl>
        <small>
          {t(row.explanationKey, {
            model: row.upstreamModel || '-',
            pattern: row.matchedExclude || '-',
          })}
        </small>
      </div>
    );
  };

  const renderOAuthView = () => (
    <section className={styles.oauthGrid}>
      <div className={styles.oauthCardStack}>
        <OAuthExcludedCard
          disableControls={disableControls}
          loadState={oauthState.excludedError}
          excluded={oauthState.excluded}
          onRetry={loadOauthExcluded}
          onAdd={() => openOauthExcludedEditor()}
          onEdit={openOauthExcludedEditor}
          onDelete={oauthState.deleteExcluded}
        />
        <OAuthModelAliasCard
          disableControls={disableControls}
          viewMode={oauthViewMode}
          onViewModeChange={setOauthViewMode}
          onAdd={() => openOauthModelAliasEditor()}
          onEditProvider={openOauthModelAliasEditor}
          onDeleteProvider={oauthState.deleteModelAlias}
          loadState={oauthState.modelAliasError}
          modelAlias={oauthState.modelAlias}
          onRetry={loadOauthModelAlias}
          allProviderModels={oauthState.allProviderModels}
          onUpdate={oauthState.handleMappingUpdate}
          onDeleteLink={oauthState.handleDeleteLink}
          onToggleFork={oauthState.handleToggleFork}
          onRenameAlias={oauthState.handleRenameAlias}
          onDeleteAlias={oauthState.handleDeleteAlias}
        />
      </div>
      <aside className={styles.rulePanel}>
        <div className={styles.previewPanelHeader}>
          <h2>{t('accounts.oauth_preview_title')}</h2>
          <p className={styles.previewScope}>{t('accounts.oauth_preview_scope')}</p>
        </div>
        <div className={styles.previewControls}>
          <Input
            label={t('accounts.oauth_preview_input_label')}
            value={oauthPreviewModel}
            onChange={(event) => {
              setOauthPreviewModel(event.target.value);
              setOauthDirectExpanded(false);
            }}
            placeholder={t('accounts.oauth_preview_placeholder')}
            aria-label={t('accounts.oauth_preview_input_label')}
          />
          <div className={styles.previewProviderFilter}>
            <label id="oauth-preview-provider-label">
              {t('accounts.oauth_preview_provider_label')}
            </label>
            <Select
              id="oauth-preview-provider"
              value={oauthPreviewProvider}
              options={oauthPreviewProviderOptions}
              onChange={(value) => {
                setOauthPreviewProvider(value);
                setOauthDirectExpanded(false);
              }}
              ariaLabelledBy="oauth-preview-provider-label"
            />
          </div>
        </div>
        <div className={styles.previewList}>
          {oauthPreviewRows.length === 0 ? (
            <p className={styles.previewEmpty}>{t('accounts.oauth_preview_empty')}</p>
          ) : oauthAffectedPreviewRows.length === 0 && oauthDirectPreviewRows.length === 0 ? (
            <p className={styles.previewEmpty}>{t('accounts.oauth_preview_provider_empty')}</p>
          ) : (
            <>
              {oauthAffectedPreviewRows.map(renderOAuthPreviewRow)}
              {!oauthPreviewProvider && oauthDirectPreviewRows.length > 0 ? (
                <button
                  type="button"
                  className={styles.previewDirectSummary}
                  data-oauth-preview-direct-summary={oauthDirectPreviewRows.length}
                  aria-expanded={oauthDirectExpanded}
                  onClick={() => setOauthDirectExpanded((current) => !current)}
                >
                  <span>
                    {t('accounts.oauth_preview_direct_summary', {
                      count: oauthDirectPreviewRows.length,
                    })}
                  </span>
                  <span>
                    {t(
                      oauthDirectExpanded
                        ? 'accounts.oauth_preview_direct_collapse'
                        : 'accounts.oauth_preview_direct_expand'
                    )}
                  </span>
                </button>
              ) : null}
              {oauthPreviewProvider || oauthDirectExpanded
                ? oauthDirectPreviewRows.map(renderOAuthPreviewRow)
                : null}
            </>
          )}
        </div>
      </aside>
    </section>
  );

  const renderPageActions = () => (
    <div className={styles.headerActions}>
      <Button
        variant="secondary"
        size="sm"
        onClick={() => void refreshActiveWorkspace()}
        disabled={loading}
      >
        <IconRefreshCw size={15} />
        {t('common.refresh')}
      </Button>
      <Button
        variant="secondary"
        size="sm"
        onClick={() => setAuthJsonPasteOpen(true)}
        disabled={disableControls || authJsonPasteSaving}
        loading={authJsonPasteSaving}
      >
        {!authJsonPasteSaving ? <IconFileText size={15} /> : null}
        {t('auth_files.paste_button')}
      </Button>
      <Button
        variant="primary"
        size="sm"
        onClick={handleUploadClick}
        disabled={disableControls || uploading}
        loading={uploading}
      >
        {!uploading ? <IconPlus size={15} /> : null}
        {t('auth_files.upload_button')}
      </Button>
      <input
        ref={fileInputRef}
        type="file"
        accept=".json,application/json"
        multiple
        hidden
        onChange={(event) => void handleFileChange(event)}
      />
    </div>
  );

  const renderActiveView = () => {
    if (activeView === 'health') return renderHealthView();
    if (activeView === 'oauth') return renderOAuthView();
    return renderAccountsOverview();
  };

  return (
    <div className={styles.container} lang={i18n.language}>
      <section className={styles.controlsPanel}>
        <div className={styles.controlsTabsRow}>
          {renderViewTabs()}
          {renderPageActions()}
        </div>
      </section>
      {activeView === 'accounts' ? (
        <section className={styles.controlsFilterPanel}>{renderToolbar()}</section>
      ) : null}
      {renderActiveView()}
      {renderMobileFilterPanel()}
      {renderFloatingBatchActions()}
      <AuthJsonPasteModal
        open={authJsonPasteOpen}
        saving={authJsonPasteSaving}
        disabled={disableControls}
        onClose={() => {
          if (!authJsonPasteSaving) setAuthJsonPasteOpen(false);
        }}
        onSave={handleSavePastedAuthJson}
      />
      <OAuthExcludedEditorModal
        open={oauthExcludedEditorProvider !== null}
        provider={oauthExcludedEditorProvider ?? ''}
        files={files}
        excluded={oauthState.excluded}
        modelAlias={oauthState.modelAlias}
        requestScope={authFilesRequestScope}
        disabled={disableControls}
        unsupported={oauthState.excludedError === 'unsupported'}
        onClose={() => setOauthExcludedEditorProvider(null)}
        onSaved={reloadOauthRules}
      />
      <OAuthModelAliasEditorModal
        open={oauthModelAliasEditorProvider !== null}
        provider={oauthModelAliasEditorProvider ?? ''}
        files={files}
        excluded={oauthState.excluded}
        modelAlias={oauthState.modelAlias}
        requestScope={authFilesRequestScope}
        disabled={disableControls}
        unsupported={oauthState.modelAliasError === 'unsupported'}
        onClose={() => setOauthModelAliasEditorProvider(null)}
        onSaved={reloadOauthRules}
      />
      <Modal
        open={batchPriorityOpen}
        onClose={() => {
          if (!batchFieldsUpdating) setBatchPriorityOpen(false);
        }}
        closeDisabled={batchFieldsUpdating}
        title={t('accounts.batch_priority_title')}
        width={420}
        footer={
          <div className={styles.batchPriorityFooter}>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setBatchPriorityOpen(false)}
              disabled={batchFieldsUpdating}
            >
              {t('common.cancel')}
            </Button>
            <Button
              size="sm"
              onClick={() => void handleBatchPrioritySave()}
              disabled={disableControls || selectedRows.length === 0 || batchFieldsUpdating}
              loading={batchFieldsUpdating}
            >
              {t('common.confirm')}
            </Button>
          </div>
        }
      >
        <div className={styles.batchPriorityModal}>
          <Input
            label={t('accounts.priority_label')}
            placeholder={t('accounts.priority_placeholder')}
            hint={t('accounts.priority_hint')}
            value={batchPriorityValue}
            onChange={(event) => setBatchPriorityValue(event.target.value)}
            disabled={disableControls || batchFieldsUpdating}
            inputMode="numeric"
            autoFocus
            onKeyDown={(event) => {
              if (event.key !== 'Enter' || disableControls || batchFieldsUpdating) return;
              void handleBatchPrioritySave();
            }}
          />
        </div>
      </Modal>
      <CodexReauthDialog
        open={Boolean(codexReauthTarget)}
        target={codexReauthTarget}
        requestScope={authFilesRequestScope}
        onClose={() => {
          codexReauthBaselineRef.current = null;
          setCodexReauthTarget(null);
        }}
        onSuccess={handleCodexReauthSuccess}
      />
    </div>
  );
}
