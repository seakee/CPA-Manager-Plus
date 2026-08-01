import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ChangeEvent,
  type RefObject,
  type SetStateAction,
} from 'react';
import { useTranslation } from 'react-i18next';
import { authFilesApi, type AuthFileFieldsPatch } from '@/services/api';
import { apiClient } from '@/services/api/client';
import { useNotificationStore } from '@/stores';
import type { AuthFileItem } from '@/types';
import { formatFileSize } from '@/utils/format';
import { MAX_AUTH_FILE_SIZE } from '@/utils/constants';
import { downloadBlob } from '@/utils/download';
import { parseTimestampMs } from '@/utils/timestamp';
import {
  buildAuthJsonFilePayloads,
  isSub2ApiAuthJsonInput,
  type AuthJsonFilePayload,
  type AuthJsonInputType,
} from '@/features/authFiles/sessionAuthConverter';
import {
  getTypeLabel,
  hasAuthFileStatusMessage,
  isHealthyAuthFile,
  isRuntimeOnlyAuthFile,
  normalizeProviderKey,
} from '@/features/authFiles/constants';
import {
  getAuthFileNameFromSelectionKey,
  getAuthFilePatchTarget,
  getAuthFileSelectionKey,
  getWholeAuthFileDeleteCandidates,
  type AuthFilePatchTarget,
} from '@/features/authFiles/model/authFilesPageModel';
import {
  clearCodexInspectionDisableOwnership,
  clearCodexInspectionDisableOwnershipForFile,
  getCodexInspectionOwnershipIdentityForFile,
} from '@/features/monitoring/model/codexInspectionOwnership';
import {
  authFileStatusMutationLockSetsOverlap,
  getAuthFileStatusSelectionKey,
  getAuthFileStatusMutationLockKeys,
  readAuthFileStatusAccountId,
  readAuthFileStatusAccountSnapshot,
  readAuthFileStatusPhysicalName,
  readAuthFileStatusProvider,
  readAuthFileStatusRuntimeId,
  resolveAuthFileStatusMutationTarget,
} from '@/utils/authFileStatusMutation';

type DeleteAllOptions = {
  filter: string;
  problemOnly: boolean;
  disabledOnly: boolean;
  healthyOnly: boolean;
  filteredFiles?: AuthFileItem[];
  onResetFilterToAll: () => void;
  onResetProblemOnly: () => void;
  onResetDisabledOnly: () => void;
  onResetHealthyOnly: () => void;
  onResetResultFilters?: () => void;
};

export type AuthFilesBatchPatchResult = {
  success: number;
  failed: number;
  failedNames: string[];
};

export type UseAuthFilesDataResult = {
  files: AuthFileItem[];
  selectedFiles: Set<string>;
  selectionCount: number;
  loading: boolean;
  error: string;
  uploading: boolean;
  authJsonPasteSaving: boolean;
  deleting: string | null;
  deletingAll: boolean;
  statusUpdating: Record<string, boolean>;
  credentialRefreshing: Record<string, boolean>;
  batchStatusUpdating: boolean;
  batchFieldsUpdating: boolean;
  fileInputRef: RefObject<HTMLInputElement | null>;
  loadFiles: (options?: { throwOnError?: boolean }) => Promise<void>;
  handleUploadClick: () => void;
  handleFileChange: (event: ChangeEvent<HTMLInputElement>) => Promise<void>;
  savePastedAuthJson: (
    type: AuthJsonInputType,
    fileName: string,
    jsonText: string
  ) => Promise<string[]>;
  handleDelete: (item: AuthFileItem) => void;
  handleDeleteAll: (options: DeleteAllOptions) => void;
  handleDownload: (name: string) => Promise<void>;
  handleCredentialRefresh: (item: AuthFileItem) => Promise<void>;
  handleStatusToggle: (item: AuthFileItem, enabled: boolean) => Promise<void>;
  toggleSelect: (key: string) => void;
  selectAllVisible: (visibleFiles: AuthFileItem[]) => void;
  invertVisibleSelection: (visibleFiles: AuthFileItem[]) => void;
  deselectAll: () => void;
  batchDownload: (names: string[]) => Promise<void>;
  batchSetStatus: (targets: AuthFilePatchTarget[], enabled: boolean) => Promise<void>;
  batchPatchFields: (
    targets: AuthFilePatchTarget[],
    fields: AuthFileFieldsPatch
  ) => Promise<AuthFilesBatchPatchResult | null>;
  batchDelete: (targets: AuthFileItem[]) => void;
};

type AuthFilePreparationFailure = {
  name: string;
  error: string;
};

export type PreparedAuthFileUpload = {
  files: File[];
  failures: AuthFilePreparationFailure[];
  convertedSourceCount: number;
};

type AuthFilePatchTargetGroup = {
  name: string;
  targets: AuthFilePatchTarget[];
};

const CREDENTIAL_REFRESH_POLL_INTERVAL_MS = 1_000;
const CREDENTIAL_REFRESH_POLL_ATTEMPTS = 15;
const CREDENTIAL_REFRESH_CLOCK_SKEW_MS = 5 * 60_000;

const getAuthFileSourceMemberKey = (file: AuthFileItem): string =>
  JSON.stringify([
    getAuthFileSelectionKey(file),
    readAuthFileStatusRuntimeId(file),
    readAuthFileStatusProvider(file),
    readAuthFileStatusAccountId(file),
    readAuthFileStatusAccountSnapshot(file),
  ]);

const getAuthFileSourceMembers = (files: AuthFileItem[], physicalName: string): AuthFileItem[] =>
  files.filter((file) => readAuthFileStatusPhysicalName(file) === physicalName);

type AuthFileDeleteSnapshot = {
  name: string;
  preferredTarget: AuthFileItem;
  members: AuthFileItem[];
};

type AuthFileDeleteExecutionResult = {
  deleted: number;
  files: string[];
  failed: Array<{ name: string; error: string }>;
};

class AuthFileMutationTargetChangedError extends Error {}

const authFileSourceMembershipMatches = (
  expectedMembers: AuthFileItem[],
  currentMembers: AuthFileItem[]
): boolean => {
  if (expectedMembers.length !== currentMembers.length) return false;
  const expectedKeys = expectedMembers.map(getAuthFileSourceMemberKey).sort();
  const currentKeys = currentMembers.map(getAuthFileSourceMemberKey).sort();
  return expectedKeys.every((key, index) => key === currentKeys[index]);
};

type ConfirmedAuthFileStatusUpdate = {
  expectedFiles: AuthFileItem[];
  disabled: boolean;
  sourceFile: boolean;
};

const applyConfirmedAuthFileStatusUpdate = (
  files: AuthFileItem[],
  update: ConfirmedAuthFileStatusUpdate
): AuthFileItem[] => {
  if (update.expectedFiles.length === 0) return files;

  const confirmedFiles = new Set<AuthFileItem>();
  if (update.sourceFile) {
    const physicalName = readAuthFileStatusPhysicalName(update.expectedFiles[0]);
    const currentMembers = getAuthFileSourceMembers(files, physicalName);
    if (!authFileSourceMembershipMatches(update.expectedFiles, currentMembers)) return files;
    currentMembers.forEach((file) => confirmedFiles.add(file));
  } else {
    update.expectedFiles.forEach((expectedFile) => {
      const resolution = resolveAuthFileStatusMutationTarget(
        files,
        getAuthFilePatchTarget(expectedFile)
      );
      if (resolution.target && resolution.failure === null && resolution.scope === 'credential') {
        confirmedFiles.add(resolution.target);
      }
    });
  }
  if (confirmedFiles.size === 0) return files;
  return files.map((file) =>
    confirmedFiles.has(file) ? { ...file, disabled: update.disabled } : file
  );
};

const buildAuthFileDeleteSnapshots = (
  files: AuthFileItem[],
  preferredTargets: AuthFileItem[]
): AuthFileDeleteSnapshot[] => {
  const snapshots: AuthFileDeleteSnapshot[] = [];
  const seen = new Set<string>();
  preferredTargets.forEach((preferredTarget) => {
    const name = readAuthFileStatusPhysicalName(preferredTarget);
    if (!name || seen.has(name)) return;
    const members = getAuthFileSourceMembers(files, name);
    if (members.length === 0) return;
    seen.add(name);
    snapshots.push({ name, preferredTarget, members });
  });
  return snapshots;
};

const resolveVerifiedAuthFileDeleteSelector = (
  freshFiles: AuthFileItem[],
  snapshot: AuthFileDeleteSnapshot
): string => {
  const freshMembers = getAuthFileSourceMembers(freshFiles, snapshot.name);
  if (!authFileSourceMembershipMatches(snapshot.members, freshMembers)) return '';

  const resolution = resolveAuthFileStatusMutationTarget(
    freshFiles,
    getAuthFilePatchTarget(snapshot.preferredTarget)
  );
  if (!resolution.target || resolution.failure !== null) return '';

  const sourceRows = freshMembers.filter(
    (file) => readAuthFileStatusRuntimeId(file) === snapshot.name
  );
  if (sourceRows.length > 1) return '';
  const deletesPhysicalFile = freshMembers.length > 1;
  const selector = deletesPhysicalFile
    ? snapshot.name
    : readAuthFileStatusRuntimeId(sourceRows[0] ?? resolution.target);
  if (!selector) return '';
  const selectorMatches = freshFiles.filter(
    (file) => readAuthFileStatusRuntimeId(file) === selector
  );
  if (deletesPhysicalFile) {
    return selectorMatches.some((file) => readAuthFileStatusPhysicalName(file) !== snapshot.name)
      ? ''
      : selector;
  }
  if (
    selectorMatches.length !== 1 ||
    readAuthFileStatusPhysicalName(selectorMatches[0]) !== snapshot.name
  ) {
    return '';
  }
  return selector;
};

const verifyPluginSourceDeleteFallback = async (
  snapshot: AuthFileDeleteSnapshot,
  selector: string,
  targetChangedError: string
): Promise<void> => {
  const response = await authFilesApi.list();
  const freshFiles = Array.isArray(response.files) ? response.files : [];
  const freshMembers = getAuthFileSourceMembers(freshFiles, snapshot.name);
  const freshSelector = resolveVerifiedAuthFileDeleteSelector(freshFiles, snapshot);
  const physicalSelectorCollides = freshFiles.some(
    (file) =>
      readAuthFileStatusRuntimeId(file) === snapshot.name &&
      readAuthFileStatusPhysicalName(file) !== snapshot.name
  );
  if (freshMembers.length !== 1 || freshSelector !== selector || physicalSelectorCollides) {
    throw new Error(targetChangedError);
  }
};

const deleteVerifiedAuthFileSnapshots = async (
  snapshots: AuthFileDeleteSnapshot[],
  targetChangedError: string,
  unconfirmedError: string
): Promise<AuthFileDeleteExecutionResult> => {
  const result: AuthFileDeleteExecutionResult = { deleted: 0, files: [], failed: [] };
  for (const snapshot of snapshots) {
    try {
      const response = await authFilesApi.list();
      const freshFiles = Array.isArray(response.files) ? response.files : [];
      const selector = resolveVerifiedAuthFileDeleteSelector(freshFiles, snapshot);
      if (!selector) {
        result.failed.push({ name: snapshot.name, error: targetChangedError });
        continue;
      }
      const identityTargets = getAuthFileSourceMembers(freshFiles, snapshot.name).map(
        getAuthFilePatchTarget
      );
      const deletion =
        selector === snapshot.name
          ? await authFilesApi.deleteFileByName(selector, snapshot.name, undefined, identityTargets)
          : await authFilesApi.deleteFileByName(
              selector,
              snapshot.name,
              () => verifyPluginSourceDeleteFallback(snapshot, selector, targetChangedError),
              identityTargets
            );
      result.deleted += deletion.deleted;
      result.files.push(...deletion.files);
      result.failed.push(...deletion.failed);
      if (deletion.deleted <= 0 && deletion.failed.length === 0) {
        result.failed.push({ name: snapshot.name, error: unconfirmedError });
      }
    } catch (error) {
      result.failed.push({
        name: snapshot.name,
        error: error instanceof Error ? error.message : unconfirmedError,
      });
    }
  }
  return {
    ...result,
    files: Array.from(new Set(result.files)),
  };
};

const readCredentialRefreshTimestamp = (item: AuthFileItem): number =>
  parseTimestampMs(item.lastRefresh ?? item['last_refresh']);

const readCredentialPlanType = (item: AuthFileItem): string => {
  const idToken =
    item.id_token && typeof item.id_token === 'object'
      ? (item.id_token as Record<string, unknown>)
      : null;
  const value =
    item.plan_type ?? item.chatgpt_plan_type ?? idToken?.plan_type ?? idToken?.chatgpt_plan_type;
  return typeof value === 'string' ? value.trim().toLowerCase() : '';
};

const findCredentialRefreshTarget = (
  files: AuthFileItem[],
  target: AuthFileItem
): AuthFileItem | undefined => {
  const snapshot = getAuthFilePatchTarget(target);
  const runtimeResolution = resolveAuthFileStatusMutationTarget(files, snapshot);
  if (
    runtimeResolution.target &&
    runtimeResolution.failure === null &&
    runtimeResolution.scope === 'credential'
  ) {
    return runtimeResolution.target;
  }

  const identityResolution = resolveAuthFileStatusMutationTarget(files, {
    ...snapshot,
    runtimeId: null,
  });
  return identityResolution.target &&
    identityResolution.failure === null &&
    identityResolution.scope === 'credential'
    ? identityResolution.target
    : undefined;
};

const hasCredentialRefreshCompleted = (
  target: AuthFileItem,
  baselineTimestamp: number,
  baselinePlanType: string,
  requestedAtMs: number
): boolean => {
  const currentPlanType = readCredentialPlanType(target);
  if (baselinePlanType && currentPlanType && currentPlanType !== baselinePlanType) {
    return true;
  }

  const currentTimestamp = readCredentialRefreshTimestamp(target);
  if (!Number.isFinite(currentTimestamp)) return false;
  if (Number.isFinite(baselineTimestamp)) return currentTimestamp > baselineTimestamp;
  return currentTimestamp >= requestedAtMs - CREDENTIAL_REFRESH_CLOCK_SKEW_MS;
};

const waitForCredentialRefreshPoll = () =>
  new Promise<void>((resolve) => {
    setTimeout(resolve, CREDENTIAL_REFRESH_POLL_INTERVAL_MS);
  });

const normalizePatchTargetAuthIndex = (
  value: AuthFilePatchTarget['authIndex']
): string | number | null => {
  if (value === undefined || value === null) return null;
  const trimmed = String(value).trim();
  if (!trimmed) return null;
  return typeof value === 'number' ? value : trimmed;
};

const normalizePatchTargetRuntimeId = (value: AuthFilePatchTarget['runtimeId']): string | null => {
  const trimmed = String(value ?? '').trim();
  return trimmed || null;
};

const normalizePatchTargetIdentityValue = (value: string | null | undefined): string | null => {
  const trimmed = String(value ?? '').trim();
  return trimmed || null;
};

const getPatchTargetKey = (target: AuthFilePatchTarget): string => {
  const authIndex = normalizePatchTargetAuthIndex(target.authIndex);
  return `${target.name}\u0000${authIndex === null ? '-' : String(authIndex)}`;
};

const getPatchTargetIdentityKey = (target: AuthFilePatchTarget): string => {
  const runtimeId = normalizePatchTargetRuntimeId(target.runtimeId);
  return runtimeId ? `runtime:${runtimeId}` : `selection:${getAuthFileStatusSelectionKey(target)}`;
};

const getPendingStatusMutationKeys = (
  pending: Map<string, number>,
  generation: number
): Set<string> =>
  new Set(
    [...pending.entries()]
      .filter(([, pendingGeneration]) => pendingGeneration === generation)
      .map(([key]) => key)
  );

const normalizeBatchPatchTargets = (
  targets: AuthFilePatchTarget[],
  getIdentityKey: (target: AuthFilePatchTarget) => string = getPatchTargetKey
): AuthFilePatchTarget[] => {
  const seen = new Set<string>();
  const normalized: AuthFilePatchTarget[] = [];

  targets.forEach((target) => {
    const name = String(target.name ?? '').trim();
    if (!name) return;
    const runtimeId = normalizePatchTargetRuntimeId(target.runtimeId);
    const authIndex = normalizePatchTargetAuthIndex(target.authIndex);
    const provider = normalizePatchTargetIdentityValue(target.provider);
    const accountId = normalizePatchTargetIdentityValue(target.accountId);
    const accountSnapshot = normalizePatchTargetIdentityValue(target.accountSnapshot);
    const normalizedTarget: AuthFilePatchTarget = {
      name,
      ...(runtimeId ? { runtimeId } : {}),
      ...(authIndex === null ? {} : { authIndex }),
      ...(provider ? { provider } : {}),
      ...(accountId ? { accountId } : {}),
      ...(accountSnapshot ? { accountSnapshot } : {}),
    };
    const key = getIdentityKey(normalizedTarget);
    if (seen.has(key)) return;
    seen.add(key);
    normalized.push(normalizedTarget);
  });

  return normalized;
};

const getStatusRequestTarget = (target: AuthFilePatchTarget): AuthFilePatchTarget => ({
  name: target.name,
  ...(target.runtimeId ? { runtimeId: target.runtimeId } : {}),
  ...(target.authIndex === undefined || target.authIndex === null
    ? {}
    : { authIndex: target.authIndex }),
  ...(target.provider ? { provider: target.provider } : {}),
  ...(target.accountId ? { accountId: target.accountId } : {}),
  ...(target.accountSnapshot ? { accountSnapshot: target.accountSnapshot } : {}),
});

const verifyPluginSourceStatusFallback = async (
  snapshotFiles: AuthFileItem[],
  target: AuthFilePatchTarget,
  targetChangedError: string,
  allowSharedSourceMutation: boolean
): Promise<AuthFilePatchTarget[]> => {
  const physicalName = String(target.name ?? '').trim();
  const runtimeId = String(target.runtimeId ?? '').trim();
  if (!physicalName || !runtimeId || runtimeId === physicalName) {
    throw new AuthFileMutationTargetChangedError(targetChangedError);
  }

  const expectedMembers = getAuthFileSourceMembers(snapshotFiles, physicalName);
  const response = await authFilesApi.list();
  const freshFiles = Array.isArray(response.files) ? response.files : [];
  const freshMembers = getAuthFileSourceMembers(freshFiles, physicalName);
  const resolution = resolveAuthFileStatusMutationTarget(freshFiles, target);
  const physicalSelectorCollides = freshFiles.some(
    (file) =>
      readAuthFileStatusRuntimeId(file) === physicalName &&
      readAuthFileStatusPhysicalName(file) !== physicalName
  );
  if (
    !authFileSourceMembershipMatches(expectedMembers, freshMembers) ||
    (expectedMembers.length > 1 && !allowSharedSourceMutation) ||
    !resolution.target ||
    resolution.failure !== null ||
    readAuthFileStatusRuntimeId(resolution.target) !== runtimeId ||
    physicalSelectorCollides
  ) {
    throw new AuthFileMutationTargetChangedError(targetChangedError);
  }
  return freshMembers.map(getAuthFilePatchTarget);
};

const setAuthFileStatusWithVerifiedPluginFallback = (
  snapshotFiles: AuthFileItem[],
  target: AuthFilePatchTarget,
  disabled: boolean,
  targetChangedError: string,
  allowSharedSourceMutation = false
) => {
  const requestTarget = getStatusRequestTarget(target);
  const physicalName = String(requestTarget.name ?? '').trim();
  const runtimeId = String(requestTarget.runtimeId ?? '').trim();
  if (runtimeId && runtimeId === physicalName) {
    const sourceIdentities = getAuthFileSourceMembers(snapshotFiles, physicalName).map(
      getAuthFilePatchTarget
    );
    return authFilesApi.setVerifiedSourceFileStatus(requestTarget, disabled, sourceIdentities);
  }
  return runtimeId && runtimeId !== physicalName
    ? authFilesApi.setStatusWithPluginSourceFallback(requestTarget, disabled, () =>
        verifyPluginSourceStatusFallback(
          snapshotFiles,
          target,
          targetChangedError,
          allowSharedSourceMutation
        )
      )
    : authFilesApi.setStatusWithPluginSourceFallback(requestTarget, disabled);
};

const groupBatchPatchTargets = (targets: AuthFilePatchTarget[]): AuthFilePatchTargetGroup[] => {
  const groups = new Map<string, AuthFilePatchTargetGroup>();

  targets.forEach((target) => {
    const group = groups.get(target.name) ?? {
      name: target.name,
      targets: [],
    };
    group.targets.push(target);
    groups.set(target.name, group);
  });

  return Array.from(groups.values());
};

export const buildPastedAuthJsonPayloads = (
  type: AuthJsonInputType,
  fileName: string,
  jsonText: string
): AuthJsonFilePayload[] => buildAuthJsonFilePayloads(type, fileName, jsonText);

const appendUploadFileNameSuffix = (fileName: string, suffix: number) => {
  const baseName = fileName.toLowerCase().endsWith('.json')
    ? fileName.slice(0, -'.json'.length)
    : fileName;
  return `${baseName}-${suffix}.json`;
};

const hasAuthFileUploadFailureStatus = (status: string) => {
  const normalizedStatus = status.trim().toLowerCase();
  return (
    normalizedStatus === 'error' || normalizedStatus === 'failed' || normalizedStatus === 'partial'
  );
};

const createUniqueConvertedAuthFiles = (
  payloads: AuthJsonFilePayload[],
  reservedFileNames: Iterable<string>
) => {
  const usedNames = new Set(Array.from(reservedFileNames, (name) => name.toLowerCase()));

  return payloads.map((payload) => {
    let fileName = payload.fileName;
    let suffix = 2;
    while (usedNames.has(fileName.toLowerCase())) {
      fileName = appendUploadFileNameSuffix(payload.fileName, suffix);
      suffix += 1;
    }
    usedNames.add(fileName.toLowerCase());
    return new File([JSON.stringify(payload.authJson)], fileName, { type: 'application/json' });
  });
};

export const prepareAuthFilesForUpload = async (files: File[]): Promise<PreparedAuthFileUpload> => {
  const ordinaryFiles: File[] = [];
  const convertedPayloads: AuthJsonFilePayload[] = [];
  const failures: AuthFilePreparationFailure[] = [];
  let convertedSourceCount = 0;

  for (const file of files) {
    let text: string;
    try {
      text = await file.text();
    } catch (err) {
      failures.push({
        name: file.name,
        error: err instanceof Error ? err.message : 'Failed to read file',
      });
      continue;
    }

    if (!isSub2ApiAuthJsonInput(text, MAX_AUTH_FILE_SIZE)) {
      ordinaryFiles.push(file);
      continue;
    }

    try {
      convertedPayloads.push(
        ...buildAuthJsonFilePayloads(
          'sub2api',
          'codex-account.json',
          text,
          new Date(),
          MAX_AUTH_FILE_SIZE
        )
      );
      convertedSourceCount += 1;
    } catch (err) {
      failures.push({
        name: file.name,
        error: err instanceof Error ? err.message : 'Failed to convert sub2api auth JSON',
      });
    }
  }

  const convertedFiles = createUniqueConvertedAuthFiles(
    convertedPayloads,
    ordinaryFiles.map((file) => file.name)
  );
  return {
    files: [...ordinaryFiles, ...convertedFiles],
    failures,
    convertedSourceCount,
  };
};

type UseAuthFilesDataOptions = {
  connectionFingerprint?: string | null;
};

export function useAuthFilesData(options: UseAuthFilesDataOptions = {}): UseAuthFilesDataResult {
  const { t } = useTranslation();
  const { showNotification, showConfirmation } = useNotificationStore();
  const connectionFingerprint = options.connectionFingerprint?.trim() ?? '';

  const [files, setFiles] = useState<AuthFileItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [uploading, setUploading] = useState(false);
  const [authJsonPasteSaving, setAuthJsonPasteSaving] = useState(false);
  const authJsonPasteSavingRef = useRef(false);
  const [deleting, setDeleting] = useState<string | null>(null);
  const [deletingAll, setDeletingAll] = useState(false);
  const [statusUpdating, setStatusUpdating] = useState<Record<string, boolean>>({});
  const [credentialRefreshing, setCredentialRefreshing] = useState<Record<string, boolean>>({});
  const [batchStatusUpdating, setBatchStatusUpdating] = useState(false);
  const [batchFieldsUpdating, setBatchFieldsUpdating] = useState(false);
  const [selectedFiles, setSelectedFiles] = useState<Set<string>>(new Set());

  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const connectionFingerprintRef = useRef(connectionFingerprint);
  const authFilesOperationGenerationRef = useRef(0);
  const loadFilesRequestRef = useRef(0);
  const filesRevisionRef = useRef(0);
  const batchStatusPendingRef = useRef<number | null>(null);
  const statusMutationPendingRef = useRef<Map<string, number>>(new Map());
  const batchFieldsPendingRef = useRef<number | null>(null);
  const credentialRefreshPendingRef = useRef<Map<string, number>>(new Map());
  const credentialRefreshGenerationRef = useRef(0);
  const selectionCount = selectedFiles.size;
  const commitFiles = useCallback((next: SetStateAction<AuthFileItem[]>) => {
    filesRevisionRef.current += 1;
    setFiles(next);
  }, []);

  useLayoutEffect(() => {
    connectionFingerprintRef.current = connectionFingerprint;
    authFilesOperationGenerationRef.current += 1;
    loadFilesRequestRef.current += 1;
    batchStatusPendingRef.current = null;
    statusMutationPendingRef.current.clear();
    batchFieldsPendingRef.current = null;
    commitFiles([]);
    setSelectedFiles(new Set());
    setLoading(true);
    setError('');
    setStatusUpdating({});
    setBatchStatusUpdating(false);
    setBatchFieldsUpdating(false);

    credentialRefreshGenerationRef.current += 1;
    credentialRefreshPendingRef.current.clear();
    setCredentialRefreshing({});

    return () => {
      authFilesOperationGenerationRef.current += 1;
      loadFilesRequestRef.current += 1;
      credentialRefreshGenerationRef.current += 1;
    };
  }, [commitFiles, connectionFingerprint]);

  const clearInspectionOwnershipForFile = useCallback(
    (fileName: string) => {
      if (!connectionFingerprint) return;
      clearCodexInspectionDisableOwnershipForFile(connectionFingerprint, fileName);
    },
    [connectionFingerprint]
  );
  const clearInspectionOwnershipForIdentity = useCallback(
    (file: AuthFileItem) => {
      if (!connectionFingerprint) return;
      clearCodexInspectionDisableOwnership(
        connectionFingerprint,
        getCodexInspectionOwnershipIdentityForFile(file)
      );
    },
    [connectionFingerprint]
  );
  const toggleSelect = useCallback((key: string) => {
    setSelectedFiles((prev) => {
      const next = new Set(prev);
      if (next.has(key)) {
        next.delete(key);
      } else {
        next.add(key);
      }
      return next;
    });
  }, []);

  const selectAllVisible = useCallback((visibleFiles: AuthFileItem[]) => {
    const nextSelected = visibleFiles
      .filter((file) => !isRuntimeOnlyAuthFile(file))
      .map(getAuthFileSelectionKey);
    if (nextSelected.length === 0) return;
    setSelectedFiles((prev) => {
      const next = new Set(prev);
      nextSelected.forEach((key) => next.add(key));
      return next;
    });
  }, []);

  const invertVisibleSelection = useCallback((visibleFiles: AuthFileItem[]) => {
    const visibleNames = visibleFiles
      .filter((file) => !isRuntimeOnlyAuthFile(file))
      .map(getAuthFileSelectionKey);
    if (visibleNames.length === 0) return;

    setSelectedFiles((prev) => {
      const next = new Set(prev);
      visibleNames.forEach((key) => {
        if (next.has(key)) {
          next.delete(key);
        } else {
          next.add(key);
        }
      });
      return next;
    });
  }, []);

  const deselectAll = useCallback(() => {
    setSelectedFiles(new Set());
  }, []);

  const applyDeletedFiles = useCallback(
    (names: string[]) => {
      const deletedNames = Array.from(new Set(names.map((name) => name.trim()).filter(Boolean)));
      if (deletedNames.length === 0) return;

      const deletedSet = new Set(deletedNames);
      deletedNames.forEach(clearInspectionOwnershipForFile);
      commitFiles((prev) => prev.filter((file) => !deletedSet.has(file.name)));
      setSelectedFiles((prev) => {
        if (prev.size === 0) return prev;
        let changed = false;
        const next = new Set<string>();
        prev.forEach((key) => {
          const name = getAuthFileNameFromSelectionKey(key);
          if (deletedSet.has(name)) {
            changed = true;
          } else {
            next.add(key);
          }
        });
        return changed ? next : prev;
      });
    },
    [clearInspectionOwnershipForFile, commitFiles]
  );

  useEffect(() => {
    if (selectedFiles.size === 0) return;
    const existingKeys = new Set(files.map(getAuthFileSelectionKey));
    setSelectedFiles((prev) => {
      let changed = false;
      const next = new Set<string>();
      prev.forEach((key) => {
        if (existingKeys.has(key)) {
          next.add(key);
        } else {
          changed = true;
        }
      });
      return changed ? next : prev;
    });
  }, [files, selectedFiles.size]);

  const loadFiles = useCallback(
    async (options?: { throwOnError?: boolean }) => {
      const requestConnectionFingerprint = connectionFingerprint;
      const generation = authFilesOperationGenerationRef.current;
      const requestID = ++loadFilesRequestRef.current;
      const isCurrentRequest = () =>
        authFilesOperationGenerationRef.current === generation &&
        connectionFingerprintRef.current === requestConnectionFingerprint &&
        loadFilesRequestRef.current === requestID;
      setLoading(true);
      setError('');
      try {
        const data = await authFilesApi.list();
        if (!isCurrentRequest()) return;
        commitFiles(data?.files || []);
      } catch (err: unknown) {
        if (!isCurrentRequest()) return;
        const errorMessage = err instanceof Error ? err.message : t('notification.refresh_failed');
        setError(errorMessage);
        if (options?.throwOnError) {
          throw err instanceof Error ? err : new Error(errorMessage);
        }
      } finally {
        if (isCurrentRequest()) setLoading(false);
      }
    },
    [commitFiles, connectionFingerprint, t]
  );

  const handleUploadClick = useCallback(() => {
    fileInputRef.current?.click();
  }, []);

  const handleFileChange = useCallback(
    async (event: ChangeEvent<HTMLInputElement>) => {
      const fileList = event.target.files;
      if (!fileList || fileList.length === 0) return;

      const filesToUpload = Array.from(fileList);
      const validFiles: File[] = [];
      const invalidFiles: string[] = [];
      const oversizedFiles: string[] = [];

      filesToUpload.forEach((file) => {
        if (!file.name.endsWith('.json')) {
          invalidFiles.push(file.name);
          return;
        }
        if (file.size > MAX_AUTH_FILE_SIZE) {
          oversizedFiles.push(file.name);
          return;
        }
        validFiles.push(file);
      });

      if (invalidFiles.length > 0) {
        showNotification(t('auth_files.upload_error_json'), 'error');
      }
      if (oversizedFiles.length > 0) {
        showNotification(
          t('auth_files.upload_error_size', { maxSize: formatFileSize(MAX_AUTH_FILE_SIZE) }),
          'error'
        );
      }

      if (validFiles.length === 0) {
        event.target.value = '';
        return;
      }

      setUploading(true);
      try {
        const prepared = await prepareAuthFilesForUpload(validFiles);
        const result =
          prepared.files.length > 0
            ? await authFilesApi.uploadFiles(prepared.files)
            : { status: 'error', uploaded: 0, files: [], failed: [] };
        const successCount = result.uploaded;
        const failures = [...prepared.failures, ...result.failed];
        const hasFailureStatus = hasAuthFileUploadFailureStatus(result.status);

        if (successCount > 0) {
          result.files.forEach(clearInspectionOwnershipForFile);
          if (!hasFailureStatus || failures.length > 0) {
            const suffix =
              prepared.files.length > 1 ? ` (${successCount}/${prepared.files.length})` : '';
            showNotification(
              `${t('auth_files.upload_success')}${suffix}`,
              failures.length ? 'warning' : 'success'
            );
          }
          await loadFiles();
        }

        if (failures.length > 0 || hasFailureStatus) {
          const details = failures.map((item) => `${item.name}: ${item.error}`).join('; ');
          showNotification(
            details
              ? `${t('notification.upload_failed')}: ${details}`
              : t('notification.upload_failed'),
            'error'
          );
        }
      } catch (err: unknown) {
        const errorMessage = err instanceof Error ? err.message : 'Unknown error';
        showNotification(`${t('notification.upload_failed')}: ${errorMessage}`, 'error');
      } finally {
        setUploading(false);
        event.target.value = '';
      }
    },
    [clearInspectionOwnershipForFile, loadFiles, showNotification, t]
  );

  const savePastedAuthJson = useCallback(
    async (type: AuthJsonInputType, fileName: string, jsonText: string) => {
      if (authJsonPasteSavingRef.current) {
        throw new Error(t('auth_files.paste_error_save_in_progress'));
      }
      authJsonPasteSavingRef.current = true;
      setAuthJsonPasteSaving(true);
      try {
        const payloads = buildPastedAuthJsonPayloads(type, fileName, jsonText);
        const savedFileNames = payloads.map((payload) => payload.fileName);
        if (payloads.length === 1) {
          try {
            await authFilesApi.saveJsonObject(payloads[0].fileName, payloads[0].authJson);
            clearInspectionOwnershipForFile(payloads[0].fileName);
          } catch {
            throw new Error(t('notification.save_failed'));
          }
        } else {
          const uploadFiles = createUniqueConvertedAuthFiles(payloads, []);
          let result;
          try {
            result = await authFilesApi.uploadFiles(uploadFiles);
          } catch {
            throw new Error(t('notification.save_failed'));
          }
          result.files.forEach(clearInspectionOwnershipForFile);
          if (
            hasAuthFileUploadFailureStatus(result.status) ||
            result.failed.length > 0 ||
            result.uploaded !== uploadFiles.length
          ) {
            const hasFailureStatus = hasAuthFileUploadFailureStatus(result.status);
            const failedNames = result.failed.map((item) => item.name);
            const unresolvedNames = uploadFiles
              .map((file) => file.name)
              .filter((name) => !result.files.includes(name) && !failedNames.includes(name));
            const affectedNames = [...failedNames, ...unresolvedNames];
            if (result.uploaded > 0) {
              try {
                await loadFiles({ throwOnError: true });
              } catch (reloadError) {
                const reloadMessage =
                  reloadError instanceof Error
                    ? reloadError.message
                    : t('notification.refresh_failed');
                showNotification(
                  `${t('notification.refresh_failed')}: ${reloadMessage}`,
                  'warning'
                );
              }
            }
            if (hasFailureStatus && affectedNames.length === 0) {
              throw new Error(t('notification.save_failed'));
            }
            throw new Error(
              t('auth_files.paste_error_partial', {
                uploaded: result.uploaded,
                total: uploadFiles.length,
                names: (affectedNames.length > 0
                  ? affectedNames
                  : uploadFiles.map((file) => file.name)
                ).join(', '),
              })
            );
          }
        }
        const showPasteSuccess = () => {
          if (savedFileNames.length === 1) {
            showNotification(t('auth_files.paste_success', { name: savedFileNames[0] }), 'success');
            return;
          }
          showNotification(
            t('auth_files.paste_success_many', { count: savedFileNames.length }),
            'success'
          );
        };
        try {
          await loadFiles({ throwOnError: true });
        } catch (reloadError) {
          const reloadMessage =
            reloadError instanceof Error ? reloadError.message : t('notification.refresh_failed');
          showPasteSuccess();
          showNotification(`${t('notification.refresh_failed')}: ${reloadMessage}`, 'warning');
          return savedFileNames;
        }
        showPasteSuccess();
        return savedFileNames;
      } catch (err) {
        throw new Error(err instanceof Error ? err.message : t('notification.save_failed'));
      } finally {
        authJsonPasteSavingRef.current = false;
        setAuthJsonPasteSaving(false);
      }
    },
    [clearInspectionOwnershipForFile, loadFiles, showNotification, t]
  );

  const handleDelete = useCallback(
    (item: AuthFileItem) => {
      const name = readAuthFileStatusPhysicalName(item);
      if (!name) {
        showNotification(t('notification.delete_failed'), 'error');
        return;
      }
      const currentMembers = getAuthFileSourceMembers(files, name);
      const expectedMembers = currentMembers.length > 0 ? currentMembers : [item];
      const deleteSnapshot: AuthFileDeleteSnapshot = {
        name,
        preferredTarget: item,
        members: expectedMembers,
      };
      const sharedFile = expectedMembers.length > 1;
      showConfirmation({
        title: t('auth_files.delete_title', { defaultValue: 'Delete File' }),
        message: sharedFile
          ? t('auth_files.delete_shared_confirm', { name, count: expectedMembers.length })
          : `${t('auth_files.delete_confirm')} "${name}" ?`,
        variant: 'danger',
        confirmText: t('common.next'),
        secondConfirmation: {
          title: t('auth_files.delete_second_title'),
          message: sharedFile
            ? t('auth_files.delete_shared_second_confirm', {
                name,
                count: expectedMembers.length,
              })
            : t('auth_files.delete_second_confirm', { name }),
          variant: 'danger',
          confirmText: t('auth_files.delete_second_action'),
        },
        onConfirm: async () => {
          setDeleting(name);
          try {
            const result = await deleteVerifiedAuthFileSnapshots(
              [deleteSnapshot],
              t('auth_files.delete_target_changed'),
              t('notification.delete_failed')
            );
            if (result.deleted <= 0 || result.files.length === 0) {
              const failure = result.failed.find((item) => item.name === name) ?? result.failed[0];
              const message = failure?.error
                ? `${t('notification.delete_failed')}: ${failure.error}`
                : t('notification.delete_failed');
              showNotification(message, 'error');
              return;
            }
            showNotification(t('auth_files.delete_success'), 'success');
            applyDeletedFiles(result.files);
          } catch (err: unknown) {
            const errorMessage = err instanceof Error ? err.message : '';
            showNotification(`${t('notification.delete_failed')}: ${errorMessage}`, 'error');
          } finally {
            setDeleting(null);
          }
        },
      });
    },
    [applyDeletedFiles, files, showConfirmation, showNotification, t]
  );

  const handleDeleteAll = useCallback(
    (deleteAllOptions: DeleteAllOptions) => {
      const {
        filter,
        problemOnly,
        disabledOnly,
        healthyOnly,
        filteredFiles,
        onResetFilterToAll,
        onResetProblemOnly,
        onResetDisabledOnly,
        onResetHealthyOnly,
        onResetResultFilters,
      } = deleteAllOptions;
      const normalizedFilter = normalizeProviderKey(filter);
      const isFiltered = normalizedFilter !== 'all';
      const isProblemOnly = problemOnly === true;
      const isDisabledOnly = disabledOnly === true;
      const isHealthyOnly = healthyOnly === true;
      const usesProvidedFilteredFiles = Array.isArray(filteredFiles);
      const isFilteredResult = usesProvidedFilteredFiles || isDisabledOnly || isHealthyOnly;
      const deletesAllFiles =
        !isFiltered &&
        !isProblemOnly &&
        !isDisabledOnly &&
        !isHealthyOnly &&
        !usesProvidedFilteredFiles;
      const typeLabel = isFiltered ? getTypeLabel(t, normalizedFilter) : t('auth_files.filter_all');
      let confirmMessage = t('auth_files.delete_all_confirm');
      if (isFilteredResult) {
        confirmMessage = t('auth_files.delete_filtered_result_confirm_file_scope');
      } else if (isProblemOnly) {
        confirmMessage = isFiltered
          ? t('auth_files.delete_problem_filtered_confirm', { type: typeLabel })
          : t('auth_files.delete_problem_confirm');
      } else if (isFiltered) {
        confirmMessage = t('auth_files.delete_filtered_confirm', { type: typeLabel });
      }

      const eligibleRows = (
        usesProvidedFilteredFiles
          ? filteredFiles
          : files.filter((file) => {
              if (
                isFiltered &&
                normalizeProviderKey(String(file.type ?? file.provider ?? '')) !== normalizedFilter
              ) {
                return false;
              }
              if (isProblemOnly && !hasAuthFileStatusMessage(file)) return false;
              if (isDisabledOnly && file.disabled !== true) return false;
              if (isHealthyOnly && !isHealthyAuthFile(file)) return false;
              return true;
            })
      ).filter((file) => !isRuntimeOnlyAuthFile(file));
      const filesToDelete = getWholeAuthFileDeleteCandidates(files, eligibleRows);

      if (filesToDelete.length === 0) {
        let emptyMessage = t('auth_files.delete_filtered_none', { type: typeLabel });
        if (isFilteredResult) {
          emptyMessage = t('auth_files.delete_filtered_result_none');
        } else if (isProblemOnly) {
          emptyMessage = isFiltered
            ? t('auth_files.delete_problem_filtered_none', { type: typeLabel })
            : t('auth_files.delete_problem_none');
        }
        showNotification(emptyMessage, 'info');
        return;
      }

      const fileNames = filesToDelete.map((file) => file.name);
      const deleteSnapshots = buildAuthFileDeleteSnapshots(files, filesToDelete);
      let deleteScope = t('auth_files.delete_scope_all');
      if (isFilteredResult) {
        deleteScope = t('auth_files.delete_scope_filtered_result');
      } else if (isProblemOnly) {
        deleteScope = isFiltered
          ? t('auth_files.delete_scope_problem_provider', { type: typeLabel })
          : t('auth_files.delete_scope_problem');
      } else if (isFiltered) {
        deleteScope = t('auth_files.delete_scope_provider', { type: typeLabel });
      }

      showConfirmation({
        title: t('auth_files.delete_all_title', { defaultValue: 'Delete All Files' }),
        message: confirmMessage,
        variant: 'danger',
        confirmText: t('common.next'),
        secondConfirmation: {
          title: t('auth_files.delete_many_second_title'),
          message: t('auth_files.delete_many_second_confirm', {
            count: fileNames.length,
            scope: deleteScope,
          }),
          variant: 'danger',
          confirmText: t('auth_files.delete_second_action'),
        },
        onConfirm: async () => {
          setDeletingAll(true);
          try {
            if (deletesAllFiles) {
              await authFilesApi.deleteAll();
              showNotification(t('auth_files.delete_all_success'), 'success');
              commitFiles((prev) => prev.filter((file) => isRuntimeOnlyAuthFile(file)));
              deselectAll();
              return;
            }

            const result = await deleteVerifiedAuthFileSnapshots(
              deleteSnapshots,
              t('auth_files.delete_target_changed'),
              t('notification.delete_failed')
            );
            const success = result.deleted;
            const failed = result.failed.length;

            applyDeletedFiles(result.files);

            if (failed === 0 && isFilteredResult) {
              showNotification(
                t('auth_files.delete_filtered_result_success', { count: success }),
                'success'
              );
            } else if (failed === 0 && isProblemOnly) {
              showNotification(
                isFiltered
                  ? t('auth_files.delete_problem_filtered_success', {
                      count: success,
                      type: typeLabel,
                    })
                  : t('auth_files.delete_problem_success', { count: success }),
                'success'
              );
            } else if (failed === 0) {
              showNotification(
                t('auth_files.delete_filtered_success', { count: success, type: typeLabel }),
                'success'
              );
            } else if (isFilteredResult) {
              showNotification(
                t('auth_files.delete_filtered_result_partial', { success, failed }),
                'warning'
              );
            } else if (isProblemOnly) {
              showNotification(
                isFiltered
                  ? t('auth_files.delete_problem_filtered_partial', {
                      success,
                      failed,
                      type: typeLabel,
                    })
                  : t('auth_files.delete_problem_partial', { success, failed }),
                'warning'
              );
            } else {
              showNotification(
                t('auth_files.delete_filtered_partial', { success, failed, type: typeLabel }),
                'warning'
              );
            }

            if (isFiltered) {
              onResetFilterToAll();
            }
            if (isProblemOnly) {
              onResetProblemOnly();
            }
            if (isDisabledOnly) {
              onResetDisabledOnly();
            }
            if (isHealthyOnly) {
              onResetHealthyOnly();
            }
            if (usesProvidedFilteredFiles) {
              onResetResultFilters?.();
            }
            deselectAll();
          } catch (err: unknown) {
            const errorMessage = err instanceof Error ? err.message : '';
            showNotification(`${t('notification.delete_failed')}: ${errorMessage}`, 'error');
          } finally {
            setDeletingAll(false);
          }
        },
      });
    },
    [applyDeletedFiles, commitFiles, deselectAll, files, showConfirmation, showNotification, t]
  );

  const handleDownload = useCallback(
    async (name: string) => {
      try {
        const response = await apiClient.getRaw(
          `/auth-files/download?name=${encodeURIComponent(name)}`,
          { responseType: 'blob' }
        );
        const blob = new Blob([response.data]);
        downloadBlob({ filename: name, blob });
        showNotification(t('auth_files.download_success'), 'success');
      } catch (err: unknown) {
        const errorMessage = err instanceof Error ? err.message : '';
        showNotification(`${t('notification.download_failed')}: ${errorMessage}`, 'error');
      }
    },
    [showNotification, t]
  );

  const handleCredentialRefresh = useCallback(
    async (item: AuthFileItem) => {
      const operationKey = getAuthFileSelectionKey(item);
      if (!operationKey || credentialRefreshPendingRef.current.has(operationKey)) return;

      const generation = credentialRefreshGenerationRef.current;

      credentialRefreshPendingRef.current.set(operationKey, generation);
      setCredentialRefreshing((prev) => ({ ...prev, [operationKey]: true }));

      try {
        const response = await authFilesApi.list();
        const currentFiles = Array.isArray(response.files) ? response.files : [];
        const resolution = resolveAuthFileStatusMutationTarget(
          currentFiles,
          getAuthFilePatchTarget(item)
        );
        if (
          !resolution.target ||
          resolution.failure !== null ||
          resolution.scope !== 'credential'
        ) {
          throw new AuthFileMutationTargetChangedError(
            t('auth_files.status_mutation_scope_ambiguous', { name: item.name })
          );
        }
        const currentFile = resolution.target;
        const currentTarget = getAuthFilePatchTarget(currentFile);
        const baselineTimestamp = readCredentialRefreshTimestamp(currentFile);
        const baselinePlanType = readCredentialPlanType(currentFile);
        const requestedAtMs = Date.now();
        commitFiles(currentFiles);

        await authFilesApi.requestCredentialRefresh(
          currentTarget,
          getAuthFileSourceMembers(currentFiles, currentFile.name).map(getAuthFilePatchTarget)
        );
        let latestFiles: AuthFileItem[] | null = null;

        for (let attempt = 0; attempt < CREDENTIAL_REFRESH_POLL_ATTEMPTS; attempt += 1) {
          if (attempt > 0) {
            await waitForCredentialRefreshPoll();
          }
          if (credentialRefreshGenerationRef.current !== generation) return;

          try {
            const data = await authFilesApi.list();
            if (credentialRefreshGenerationRef.current !== generation) return;
            latestFiles = data?.files || [];
            const refreshedTarget = findCredentialRefreshTarget(latestFiles, currentFile);
            if (
              refreshedTarget &&
              hasCredentialRefreshCompleted(
                refreshedTarget,
                baselineTimestamp,
                baselinePlanType,
                requestedAtMs
              )
            ) {
              commitFiles(latestFiles);
              showNotification(
                t('auth_files.credential_refresh_completed', { name: item.name }),
                'success'
              );
              return;
            }
          } catch {
            // CPA accepted the refresh request; transient status polling failures can retry.
          }
        }

        if (credentialRefreshGenerationRef.current !== generation) return;
        if (latestFiles) commitFiles(latestFiles);
        showNotification(
          t('auth_files.credential_refresh_pending', { name: item.name }),
          'warning'
        );
      } catch (err: unknown) {
        if (credentialRefreshGenerationRef.current !== generation) return;
        const message = err instanceof Error ? err.message : t('common.unknown_error');
        showNotification(
          t('auth_files.credential_refresh_failed', { name: item.name, message }),
          'error'
        );
      } finally {
        if (credentialRefreshPendingRef.current.get(operationKey) === generation) {
          credentialRefreshPendingRef.current.delete(operationKey);
        }
        if (credentialRefreshGenerationRef.current === generation) {
          setCredentialRefreshing((prev) => {
            if (!prev[operationKey]) return prev;
            const next = { ...prev };
            delete next[operationKey];
            return next;
          });
        }
      }
    },
    [commitFiles, showNotification, t]
  );

  const handleStatusToggle = useCallback(
    async (item: AuthFileItem, enabled: boolean) => {
      const generation = authFilesOperationGenerationRef.current;
      const filesRevision = filesRevisionRef.current;
      const name = item.name;
      const requestedTarget = getAuthFilePatchTarget(item);
      const operationKey = getAuthFileSelectionKey(item);
      const nextDisabled = !enabled;
      const lockedKeys = getAuthFileStatusMutationLockKeys(files, requestedTarget);
      const displayKeys = new Set([operationKey]);
      let affectedFiles: AuthFileItem[] = [];

      if (
        authFileStatusMutationLockSetsOverlap(
          lockedKeys,
          getPendingStatusMutationKeys(statusMutationPendingRef.current, generation)
        )
      ) {
        return;
      }
      lockedKeys.forEach((key) => statusMutationPendingRef.current.set(key, generation));
      setStatusUpdating((prev) => ({ ...prev, [operationKey]: true }));

      try {
        const response = await authFilesApi.list();
        if (authFilesOperationGenerationRef.current !== generation) return;
        const currentFiles = Array.isArray(response.files) ? response.files : [];
        if (filesRevisionRef.current === filesRevision) commitFiles(currentFiles);
        const resolution = resolveAuthFileStatusMutationTarget(currentFiles, requestedTarget);
        const currentFile = resolution.target;
        if (
          !currentFile ||
          resolution.failure === 'not-found' ||
          resolution.failure === 'identity-changed'
        ) {
          showNotification(t('notification.update_failed'), 'error');
          return;
        }
        if (resolution.failure === 'runtime-id-changed') {
          showNotification(t('notification.update_failed'), 'error');
          return;
        }
        if (
          resolution.failure === 'ambiguous' ||
          resolution.scope === 'ambiguous' ||
          resolution.scope === 'expanded-child'
        ) {
          showNotification(t('auth_files.status_mutation_scope_ambiguous', { name }), 'warning');
          return;
        }

        const currentTarget = getAuthFilePatchTarget(currentFile);
        const refreshedLockKeys = getAuthFileStatusMutationLockKeys(currentFiles, currentTarget);
        const foreignLocks = new Set(
          [...getPendingStatusMutationKeys(statusMutationPendingRef.current, generation)].filter(
            (key) => !lockedKeys.has(key)
          )
        );
        if (authFileStatusMutationLockSetsOverlap(refreshedLockKeys, foreignLocks)) {
          showNotification(t('notification.update_failed'), 'error');
          return;
        }
        refreshedLockKeys.forEach((key) => {
          lockedKeys.add(key);
          statusMutationPendingRef.current.set(key, generation);
        });

        affectedFiles =
          resolution.scope === 'source-file' ? resolution.affectedFiles : [currentFile];
        affectedFiles.forEach((file) => {
          displayKeys.add(getAuthFileSelectionKey(file));
        });
        setStatusUpdating((prev) => {
          const next = { ...prev };
          displayKeys.forEach((key) => {
            next[key] = true;
          });
          return next;
        });

        const res = await setAuthFileStatusWithVerifiedPluginFallback(
          currentFiles,
          currentTarget,
          nextDisabled,
          t('auth_files.status_mutation_scope_ambiguous', { name: currentFile.name })
        );
        if (authFilesOperationGenerationRef.current !== generation) return;
        const sourceFileMutation =
          resolution.scope === 'source-file' || res.mutationScope === 'source-file';
        const confirmedAffectedFiles = sourceFileMutation
          ? currentFiles.filter(
              (file) => readAuthFileStatusPhysicalName(file) === currentFile.name.trim()
            )
          : affectedFiles;
        commitFiles((prev) =>
          applyConfirmedAuthFileStatusUpdate(prev, {
            expectedFiles: confirmedAffectedFiles,
            disabled: res.disabled,
            sourceFile: sourceFileMutation,
          })
        );
        if (sourceFileMutation) {
          clearInspectionOwnershipForFile(currentFile.name);
        } else {
          clearInspectionOwnershipForIdentity(currentFile);
        }
        showNotification(
          enabled
            ? t('auth_files.status_enabled_success', { name })
            : t('auth_files.status_disabled_success', { name }),
          'success'
        );
      } catch (err: unknown) {
        if (authFilesOperationGenerationRef.current !== generation) return;
        const errorMessage = err instanceof Error ? err.message : '';
        showNotification(`${t('notification.update_failed')}: ${errorMessage}`, 'error');
      } finally {
        lockedKeys.forEach((key) => {
          if (statusMutationPendingRef.current.get(key) === generation) {
            statusMutationPendingRef.current.delete(key);
          }
        });
        if (authFilesOperationGenerationRef.current === generation) {
          setStatusUpdating((prev) => {
            const next = { ...prev };
            displayKeys.forEach((key) => {
              delete next[key];
            });
            return next;
          });
        }
      }
    },
    [
      clearInspectionOwnershipForFile,
      clearInspectionOwnershipForIdentity,
      commitFiles,
      files,
      showNotification,
      t,
    ]
  );

  const batchSetStatus = useCallback(
    async (targets: AuthFilePatchTarget[], enabled: boolean) => {
      const generation = authFilesOperationGenerationRef.current;
      const filesRevision = filesRevisionRef.current;
      if (batchStatusPendingRef.current !== null) return;

      const normalizedTargets = normalizeBatchPatchTargets(targets, getPatchTargetIdentityKey);
      if (normalizedTargets.length === 0) return;
      const nextDisabled = !enabled;
      const lockedKeys = new Set<string>();
      normalizedTargets.forEach((target) => {
        getAuthFileStatusMutationLockKeys(files, target).forEach((key) => lockedKeys.add(key));
      });
      if (
        authFileStatusMutationLockSetsOverlap(
          lockedKeys,
          getPendingStatusMutationKeys(statusMutationPendingRef.current, generation)
        )
      ) {
        return;
      }
      const displayKeys = new Set(normalizedTargets.map(getAuthFileStatusSelectionKey));

      batchStatusPendingRef.current = generation;
      lockedKeys.forEach((key) => statusMutationPendingRef.current.set(key, generation));
      setBatchStatusUpdating(true);
      setStatusUpdating((prev) => {
        const next = { ...prev };
        displayKeys.forEach((key) => {
          next[key] = true;
        });
        return next;
      });

      try {
        const response = await authFilesApi.list();
        if (authFilesOperationGenerationRef.current !== generation) return;
        const currentFiles = Array.isArray(response.files) ? response.files : [];
        if (filesRevisionRef.current === filesRevision) commitFiles(currentFiles);

        type ResolvedStatusEntry = {
          file: AuthFileItem;
          target: AuthFilePatchTarget;
          scope: 'credential' | 'source-file' | 'expanded-child';
          affectedFiles: AuthFileItem[];
        };
        type ExecutableStatusEntry = ResolvedStatusEntry & { selectedCount: number };
        const resolvedEntries: ResolvedStatusEntry[] = [];
        const seenRuntimeIds = new Set<string>();
        let failCount = 0;
        let needsReviewCount = 0;

        normalizedTargets.forEach((target) => {
          const resolution = resolveAuthFileStatusMutationTarget(currentFiles, target);
          const file = resolution.target;
          if (resolution.failure === 'ambiguous') {
            needsReviewCount++;
            return;
          }
          if (
            !file ||
            resolution.failure === 'not-found' ||
            resolution.failure === 'runtime-id-changed' ||
            resolution.failure === 'identity-changed'
          ) {
            failCount++;
            return;
          }
          if (resolution.scope === 'ambiguous') {
            needsReviewCount++;
            return;
          }

          const currentTarget = getAuthFilePatchTarget(file);
          const refreshedLockKeys = getAuthFileStatusMutationLockKeys(currentFiles, currentTarget);
          const hasForeignLock = [...refreshedLockKeys].some(
            (key) =>
              statusMutationPendingRef.current.get(key) === generation && !lockedKeys.has(key)
          );
          if (hasForeignLock) {
            failCount++;
            return;
          }
          refreshedLockKeys.forEach((key) => {
            lockedKeys.add(key);
            statusMutationPendingRef.current.set(key, generation);
          });
          displayKeys.add(getAuthFileSelectionKey(file));

          const runtimeId = readAuthFileStatusRuntimeId(file);
          if (runtimeId && seenRuntimeIds.has(runtimeId)) return;
          if (runtimeId) seenRuntimeIds.add(runtimeId);
          resolvedEntries.push({
            file,
            target: currentTarget,
            scope: resolution.scope,
            affectedFiles: resolution.scope === 'source-file' ? resolution.affectedFiles : [file],
          });
        });

        const sourceEntriesByFile = new Map<string, ResolvedStatusEntry>();
        resolvedEntries.forEach((entry) => {
          if (entry.scope !== 'source-file') return;
          const fileName = String(entry.file.name ?? '').trim();
          if (!sourceEntriesByFile.has(fileName)) sourceEntriesByFile.set(fileName, entry);
        });
        const resolvedCountByPhysicalFile = new Map<string, number>();
        resolvedEntries.forEach((entry) => {
          const fileName = readAuthFileStatusPhysicalName(entry.file);
          resolvedCountByPhysicalFile.set(
            fileName,
            (resolvedCountByPhysicalFile.get(fileName) ?? 0) + 1
          );
        });
        const executableEntries: ExecutableStatusEntry[] = [];
        const addedSourceFiles = new Set<string>();
        resolvedEntries.forEach((entry) => {
          const fileName = String(entry.file.name ?? '').trim();
          if (entry.scope === 'expanded-child') {
            if (!sourceEntriesByFile.has(fileName)) needsReviewCount++;
            return;
          }
          if (entry.scope === 'source-file') {
            if (addedSourceFiles.has(fileName)) return;
            addedSourceFiles.add(fileName);
          }
          executableEntries.push({
            ...entry,
            selectedCount:
              entry.scope === 'source-file' ? (resolvedCountByPhysicalFile.get(fileName) ?? 1) : 1,
          });
        });

        executableEntries.forEach((entry) => {
          entry.affectedFiles.forEach((file) => {
            displayKeys.add(getAuthFileSelectionKey(file));
          });
        });
        setStatusUpdating((prev) => {
          const next = { ...prev };
          displayKeys.forEach((key) => {
            next[key] = true;
          });
          return next;
        });
        type StatusExecutionResult = Awaited<
          ReturnType<typeof authFilesApi.setStatusWithPluginSourceFallback>
        >;
        type StatusExecutionOutcome = {
          entry: ExecutableStatusEntry;
          result: PromiseSettledResult<StatusExecutionResult>;
        };
        const entriesByPhysicalFile = new Map<string, ExecutableStatusEntry[]>();
        executableEntries.forEach((entry) => {
          const fileName = readAuthFileStatusPhysicalName(entry.file);
          const group = entriesByPhysicalFile.get(fileName) ?? [];
          group.push(entry);
          entriesByPhysicalFile.set(fileName, group);
        });
        const groupedOutcomes = await Promise.all(
          [...entriesByPhysicalFile.values()].map(async (entries) => {
            const outcomes: StatusExecutionOutcome[] = [];
            const physicalName = entries[0] ? readAuthFileStatusPhysicalName(entries[0].file) : '';
            const allowSharedSourceMutation = authFileSourceMembershipMatches(
              getAuthFileSourceMembers(currentFiles, physicalName),
              entries.map((entry) => entry.file)
            );
            for (let index = 0; index < entries.length; index++) {
              if (authFilesOperationGenerationRef.current !== generation) break;
              const entry = entries[index];
              try {
                const value = await setAuthFileStatusWithVerifiedPluginFallback(
                  currentFiles,
                  entry.target,
                  nextDisabled,
                  t('auth_files.status_mutation_scope_ambiguous', { name: entry.file.name }),
                  allowSharedSourceMutation
                );
                outcomes.push({ entry, result: { status: 'fulfilled', value } });
                if (authFilesOperationGenerationRef.current !== generation) break;
                if (value.mutationScope === 'source-file') break;
              } catch (reason: unknown) {
                outcomes.push({ entry, result: { status: 'rejected', reason } });
                if (reason instanceof AuthFileMutationTargetChangedError) {
                  entries.slice(index + 1).forEach((remainingEntry) => {
                    outcomes.push({
                      entry: remainingEntry,
                      result: { status: 'rejected', reason },
                    });
                  });
                  break;
                }
              }
            }
            const sourceFileOutcome = outcomes.find(
              ({ entry, result }) =>
                result.status === 'fulfilled' &&
                (entry.scope === 'source-file' || result.value.mutationScope === 'source-file')
            );
            return sourceFileOutcome
              ? [
                  {
                    ...sourceFileOutcome,
                    entry: {
                      ...sourceFileOutcome.entry,
                      selectedCount: entries.reduce(
                        (count, entry) => count + entry.selectedCount,
                        0
                      ),
                    },
                  },
                ]
              : outcomes;
          })
        );
        const results = groupedOutcomes.flat();
        if (authFilesOperationGenerationRef.current !== generation) return;

        let successCount = 0;
        const confirmedUpdates: ConfirmedAuthFileStatusUpdate[] = [];

        results.forEach(({ entry, result }) => {
          if (result.status === 'fulfilled') {
            successCount += entry.selectedCount;
            const sourceFileMutation =
              entry.scope === 'source-file' || result.value.mutationScope === 'source-file';
            const confirmedFiles = sourceFileMutation
              ? currentFiles.filter(
                  (file) =>
                    readAuthFileStatusPhysicalName(file) ===
                    readAuthFileStatusPhysicalName(entry.file)
                )
              : entry.affectedFiles;
            confirmedUpdates.push({
              expectedFiles: confirmedFiles,
              disabled: result.value.disabled,
              sourceFile: sourceFileMutation,
            });
            if (sourceFileMutation) {
              clearInspectionOwnershipForFile(entry.file.name);
            } else {
              clearInspectionOwnershipForIdentity(entry.file);
            }
          } else {
            failCount += entry.selectedCount;
          }
        });

        if (confirmedUpdates.length > 0) {
          commitFiles((prev) =>
            confirmedUpdates.reduce(
              (currentFiles, update) => applyConfirmedAuthFileStatusUpdate(currentFiles, update),
              prev
            )
          );
        }

        if (needsReviewCount > 0) {
          showNotification(
            t('auth_files.batch_status_needs_review', {
              success: successCount,
              failed: failCount,
              review: needsReviewCount,
            }),
            'warning'
          );
        } else if (failCount === 0) {
          showNotification(
            t('auth_files.batch_status_success', { count: successCount }),
            'success'
          );
        } else {
          showNotification(
            t('auth_files.batch_status_partial', { success: successCount, failed: failCount }),
            'warning'
          );
        }

        deselectAll();
      } catch (err: unknown) {
        if (authFilesOperationGenerationRef.current !== generation) return;
        const errorMessage = err instanceof Error ? err.message : '';
        showNotification(`${t('notification.update_failed')}: ${errorMessage}`, 'error');
      } finally {
        if (batchStatusPendingRef.current === generation) {
          batchStatusPendingRef.current = null;
        }
        lockedKeys.forEach((key) => {
          if (statusMutationPendingRef.current.get(key) === generation) {
            statusMutationPendingRef.current.delete(key);
          }
        });
        if (authFilesOperationGenerationRef.current === generation) {
          setBatchStatusUpdating(false);
          setStatusUpdating((prev) => {
            const next = { ...prev };
            displayKeys.forEach((key) => {
              delete next[key];
            });
            return next;
          });
        }
      }
    },
    [
      clearInspectionOwnershipForFile,
      clearInspectionOwnershipForIdentity,
      commitFiles,
      deselectAll,
      files,
      showNotification,
      t,
    ]
  );

  const batchPatchFields = useCallback(
    async (
      targets: AuthFilePatchTarget[],
      fields: AuthFileFieldsPatch
    ): Promise<AuthFilesBatchPatchResult | null> => {
      const generation = authFilesOperationGenerationRef.current;
      const filesRevision = filesRevisionRef.current;
      if (batchFieldsPendingRef.current !== null) return null;

      const normalizedTargets = normalizeBatchPatchTargets(targets);
      if (normalizedTargets.length === 0) return null;
      if (Object.keys(fields).length === 0) return null;

      batchFieldsPendingRef.current = generation;
      setBatchFieldsUpdating(true);

      try {
        const response = await authFilesApi.list();
        if (authFilesOperationGenerationRef.current !== generation) return null;
        const currentFiles = Array.isArray(response.files) ? response.files : [];
        if (filesRevisionRef.current === filesRevision) commitFiles(currentFiles);

        let success = 0;
        let failed = 0;
        const failedNames = new Set<string>();
        const resolvedTargets: AuthFilePatchTarget[] = [];
        normalizedTargets.forEach((target) => {
          const resolution = resolveAuthFileStatusMutationTarget(currentFiles, target);
          if (
            !resolution.target ||
            resolution.failure !== null ||
            resolution.scope === 'ambiguous'
          ) {
            failed++;
            failedNames.add(target.name);
            return;
          }
          resolvedTargets.push(getAuthFilePatchTarget(resolution.target));
        });

        const executableGroups: Array<
          AuthFilePatchTargetGroup & { sourceIdentities: AuthFilePatchTarget[] }
        > = [];
        groupBatchPatchTargets(resolvedTargets).forEach((group) => {
          const sourceMembers = getAuthFileSourceMembers(currentFiles, group.name);
          const hasStableAuthIndexes = group.targets.every(
            (target) => normalizePatchTargetAuthIndex(target.authIndex) !== null
          );
          if (
            sourceMembers.length === 0 ||
            group.targets.length === 0 ||
            (sourceMembers.length > 1 && !hasStableAuthIndexes) ||
            (sourceMembers.length === 1 && group.targets.length !== 1)
          ) {
            failed += group.targets.length;
            failedNames.add(group.name);
            return;
          }
          executableGroups.push({
            ...group,
            sourceIdentities: sourceMembers.map(getAuthFilePatchTarget),
          });
        });

        const results = await Promise.allSettled(
          executableGroups.map((group) => {
            if (group.sourceIdentities.length === 1) {
              return authFilesApi.patchFieldsWithPluginSourceFallback(
                group.targets[0],
                fields,
                group.sourceIdentities
              );
            }
            return authFilesApi.patchFieldsForAuthIndexes(
              group.name,
              group.targets,
              group.sourceIdentities,
              fields
            );
          })
        );
        if (authFilesOperationGenerationRef.current !== generation) return null;

        results.forEach((result, index) => {
          const group = executableGroups[index];
          if (result.status === 'fulfilled') {
            success += group.targets.length;
            return;
          }
          failed += group.targets.length;
          failedNames.add(group.name);
        });

        if (success > 0) {
          try {
            await loadFiles({ throwOnError: true });
            if (authFilesOperationGenerationRef.current !== generation) return null;
          } catch (err: unknown) {
            if (authFilesOperationGenerationRef.current !== generation) return null;
            const errorMessage =
              err instanceof Error ? err.message : t('notification.refresh_failed');
            showNotification(`${t('notification.refresh_failed')}: ${errorMessage}`, 'warning');
          }
        }

        if (failed === 0) {
          showNotification(t('auth_files.batch_fields_success', { count: success }), 'success');
        } else {
          showNotification(t('auth_files.batch_fields_partial', { success, failed }), 'warning');
        }

        deselectAll();
        return { success, failed, failedNames: Array.from(failedNames) };
      } finally {
        if (batchFieldsPendingRef.current === generation) {
          batchFieldsPendingRef.current = null;
        }
        if (authFilesOperationGenerationRef.current === generation) {
          setBatchFieldsUpdating(false);
        }
      }
    },
    [commitFiles, deselectAll, loadFiles, showNotification, t]
  );

  const batchDownload = useCallback(
    async (names: string[]) => {
      const uniqueNames = Array.from(new Set(names));
      if (uniqueNames.length === 0) return;

      let successCount = 0;
      let failCount = 0;

      for (const name of uniqueNames) {
        try {
          const response = await apiClient.getRaw(
            `/auth-files/download?name=${encodeURIComponent(name)}`,
            { responseType: 'blob' }
          );
          const blob = new Blob([response.data]);
          downloadBlob({ filename: name, blob });
          successCount++;
        } catch {
          failCount++;
        }
      }

      if (failCount === 0) {
        showNotification(
          t('auth_files.batch_download_success', { count: successCount }),
          'success'
        );
      } else {
        showNotification(
          t('auth_files.batch_download_partial', { success: successCount, failed: failCount }),
          'warning'
        );
      }
    },
    [showNotification, t]
  );

  const batchDelete = useCallback(
    (targets: AuthFileItem[]) => {
      const uniqueNames = Array.from(
        new Set(targets.map(readAuthFileStatusPhysicalName).filter(Boolean))
      );
      if (uniqueNames.length === 0) return;
      const hasPartialSelection = uniqueNames.some((name) => {
        const selectedMembers = targets.filter(
          (file) => readAuthFileStatusPhysicalName(file) === name
        );
        return !authFileSourceMembershipMatches(
          getAuthFileSourceMembers(files, name),
          selectedMembers
        );
      });
      const deleteSnapshots = buildAuthFileDeleteSnapshots(files, targets);
      if (hasPartialSelection || deleteSnapshots.length !== uniqueNames.length) {
        showNotification(
          `${t('notification.delete_failed')}: ${t('auth_files.delete_target_changed')}`,
          'error'
        );
        return;
      }

      showConfirmation({
        title: t('auth_files.batch_delete_title'),
        message: t('auth_files.batch_delete_confirm', { count: uniqueNames.length }),
        variant: 'danger',
        confirmText: t('common.next'),
        secondConfirmation: {
          title: t('auth_files.delete_many_second_title'),
          message: t('auth_files.delete_many_second_confirm', {
            count: uniqueNames.length,
            scope: t('auth_files.delete_scope_selected'),
          }),
          variant: 'danger',
          confirmText: t('auth_files.delete_second_action'),
        },
        onConfirm: async () => {
          try {
            const result = await deleteVerifiedAuthFileSnapshots(
              deleteSnapshots,
              t('auth_files.delete_target_changed'),
              t('notification.delete_failed')
            );
            applyDeletedFiles(result.files);

            if (result.failed.length === 0) {
              showNotification(
                `${t('auth_files.delete_all_success')} (${result.deleted})`,
                'success'
              );
            } else {
              showNotification(
                t('auth_files.delete_filtered_partial', {
                  success: result.deleted,
                  failed: result.failed.length,
                  type: t('auth_files.filter_all'),
                }),
                'warning'
              );
            }
          } catch (err: unknown) {
            const errorMessage = err instanceof Error ? err.message : '';
            showNotification(`${t('notification.delete_failed')}: ${errorMessage}`, 'error');
          }
        },
      });
    },
    [applyDeletedFiles, files, showConfirmation, showNotification, t]
  );

  return {
    files,
    selectedFiles,
    selectionCount,
    loading,
    error,
    uploading,
    authJsonPasteSaving,
    deleting,
    deletingAll,
    statusUpdating,
    credentialRefreshing,
    batchStatusUpdating,
    batchFieldsUpdating,
    fileInputRef,
    loadFiles,
    handleUploadClick,
    handleFileChange,
    savePastedAuthJson,
    handleDelete,
    handleDeleteAll,
    handleDownload,
    handleCredentialRefresh,
    handleStatusToggle,
    toggleSelect,
    selectAllVisible,
    invertVisibleSelection,
    deselectAll,
    batchDownload,
    batchSetStatus,
    batchPatchFields,
    batchDelete,
  };
}
