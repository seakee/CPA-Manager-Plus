import { useCallback, useEffect, useRef, useState, type ChangeEvent, type RefObject } from 'react';
import { useTranslation } from 'react-i18next';
import { authFilesApi, type AuthFileFieldsPatch } from '@/services/api';
import { apiClient } from '@/services/api/client';
import { useNotificationStore } from '@/stores';
import type { AuthFileItem } from '@/types';
import { formatFileSize } from '@/utils/format';
import { MAX_AUTH_FILE_SIZE } from '@/utils/constants';
import { downloadBlob } from '@/utils/download';
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
  getAuthFileSelectionKey,
  type AuthFilePatchTarget,
} from '@/features/authFiles/model/authFilesPageModel';
import { clearCodexInspectionDisableOwnership } from '@/features/monitoring/model/codexInspectionOwnership';

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
  handleDelete: (name: string) => void;
  handleDeleteAll: (options: DeleteAllOptions) => void;
  handleDownload: (name: string) => Promise<void>;
  handleStatusToggle: (item: AuthFileItem, enabled: boolean) => Promise<void>;
  toggleSelect: (key: string) => void;
  selectAllVisible: (visibleFiles: AuthFileItem[]) => void;
  invertVisibleSelection: (visibleFiles: AuthFileItem[]) => void;
  deselectAll: () => void;
  batchDownload: (names: string[]) => Promise<void>;
  batchSetStatus: (names: string[], enabled: boolean) => Promise<void>;
  batchPatchFields: (
    targets: AuthFilePatchTarget[],
    fields: AuthFileFieldsPatch
  ) => Promise<AuthFilesBatchPatchResult | null>;
  batchDelete: (names: string[]) => void;
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
  authIndexes: Array<string | number>;
};

const normalizePatchTargetAuthIndex = (
  value: AuthFilePatchTarget['authIndex']
): string | number | null => {
  if (value === undefined || value === null) return null;
  const trimmed = String(value).trim();
  if (!trimmed) return null;
  return typeof value === 'number' ? value : trimmed;
};

const getPatchTargetKey = (target: AuthFilePatchTarget): string => {
  const authIndex = normalizePatchTargetAuthIndex(target.authIndex);
  return `${target.name}\u0000${authIndex === null ? '-' : String(authIndex)}`;
};

const normalizeBatchPatchTargets = (targets: AuthFilePatchTarget[]): AuthFilePatchTarget[] => {
  const seen = new Set<string>();
  const normalized: AuthFilePatchTarget[] = [];

  targets.forEach((target) => {
    const name = String(target.name ?? '').trim();
    if (!name) return;
    const authIndex = normalizePatchTargetAuthIndex(target.authIndex);
    const normalizedTarget = authIndex === null ? { name } : { name, authIndex };
    const key = getPatchTargetKey(normalizedTarget);
    if (seen.has(key)) return;
    seen.add(key);
    normalized.push(normalizedTarget);
  });

  return normalized;
};

const groupBatchPatchTargets = (targets: AuthFilePatchTarget[]): AuthFilePatchTargetGroup[] => {
  const groups = new Map<string, AuthFilePatchTargetGroup>();

  targets.forEach((target) => {
    const group = groups.get(target.name) ?? {
      name: target.name,
      targets: [],
      authIndexes: [],
    };
    group.targets.push(target);
    const authIndex = normalizePatchTargetAuthIndex(target.authIndex);
    if (authIndex !== null) {
      group.authIndexes.push(authIndex);
    }
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

  const [files, setFiles] = useState<AuthFileItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [uploading, setUploading] = useState(false);
  const [authJsonPasteSaving, setAuthJsonPasteSaving] = useState(false);
  const authJsonPasteSavingRef = useRef(false);
  const [deleting, setDeleting] = useState<string | null>(null);
  const [deletingAll, setDeletingAll] = useState(false);
  const [statusUpdating, setStatusUpdating] = useState<Record<string, boolean>>({});
  const [batchStatusUpdating, setBatchStatusUpdating] = useState(false);
  const [batchFieldsUpdating, setBatchFieldsUpdating] = useState(false);
  const [selectedFiles, setSelectedFiles] = useState<Set<string>>(new Set());

  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const batchStatusPendingRef = useRef(false);
  const batchFieldsPendingRef = useRef(false);
  const selectionCount = selectedFiles.size;
  const clearInspectionOwnershipForFile = useCallback(
    (fileName: string) => {
      const scope = options.connectionFingerprint?.trim();
      if (!scope) return;
      clearCodexInspectionDisableOwnership(scope, fileName);
    },
    [options.connectionFingerprint]
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
      setFiles((prev) => prev.filter((file) => !deletedSet.has(file.name)));
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
    [clearInspectionOwnershipForFile]
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
      setLoading(true);
      setError('');
      try {
        const data = await authFilesApi.list();
        setFiles(data?.files || []);
      } catch (err: unknown) {
        const errorMessage = err instanceof Error ? err.message : t('notification.refresh_failed');
        setError(errorMessage);
        if (options?.throwOnError) {
          throw err instanceof Error ? err : new Error(errorMessage);
        }
      } finally {
        setLoading(false);
      }
    },
    [t]
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
    (name: string) => {
      showConfirmation({
        title: t('auth_files.delete_title', { defaultValue: 'Delete File' }),
        message: `${t('auth_files.delete_confirm')} "${name}" ?`,
        variant: 'danger',
        confirmText: t('common.confirm'),
        onConfirm: async () => {
          setDeleting(name);
          try {
            const result = await authFilesApi.deleteFile(name);
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
    [applyDeletedFiles, showConfirmation, showNotification, t]
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

      showConfirmation({
        title: t('auth_files.delete_all_title', { defaultValue: 'Delete All Files' }),
        message: confirmMessage,
        variant: 'danger',
        confirmText: t('common.confirm'),
        onConfirm: async () => {
          setDeletingAll(true);
          try {
            if (
              !isFiltered &&
              !isProblemOnly &&
              !isDisabledOnly &&
              !isHealthyOnly &&
              !usesProvidedFilteredFiles
            ) {
              await authFilesApi.deleteAll();
              showNotification(t('auth_files.delete_all_success'), 'success');
              setFiles((prev) => prev.filter((file) => isRuntimeOnlyAuthFile(file)));
              deselectAll();
            } else {
              const filesToDelete = (
                usesProvidedFilteredFiles
                  ? filteredFiles
                  : files.filter((file) => {
                      if (
                        isFiltered &&
                        normalizeProviderKey(String(file.type ?? file.provider ?? '')) !==
                          normalizedFilter
                      ) {
                        return false;
                      }
                      if (isProblemOnly && !hasAuthFileStatusMessage(file)) return false;
                      if (isDisabledOnly && file.disabled !== true) return false;
                      if (isHealthyOnly && !isHealthyAuthFile(file)) return false;
                      return true;
                    })
              ).filter((file) => !isRuntimeOnlyAuthFile(file));

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
                setDeletingAll(false);
                return;
              }

              const result = await authFilesApi.deleteFiles(filesToDelete.map((file) => file.name));
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
            }
          } catch (err: unknown) {
            const errorMessage = err instanceof Error ? err.message : '';
            showNotification(`${t('notification.delete_failed')}: ${errorMessage}`, 'error');
          } finally {
            setDeletingAll(false);
          }
        },
      });
    },
    [applyDeletedFiles, deselectAll, files, showConfirmation, showNotification, t]
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

  const handleStatusToggle = useCallback(
    async (item: AuthFileItem, enabled: boolean) => {
      const name = item.name;
      const nextDisabled = !enabled;
      const previousDisabled = item.disabled === true;

      setStatusUpdating((prev) => ({ ...prev, [name]: true }));
      setFiles((prev) => prev.map((f) => (f.name === name ? { ...f, disabled: nextDisabled } : f)));

      try {
        const res = await authFilesApi.setStatus(name, nextDisabled);
        setFiles((prev) =>
          prev.map((f) => (f.name === name ? { ...f, disabled: res.disabled } : f))
        );
        clearInspectionOwnershipForFile(name);
        showNotification(
          enabled
            ? t('auth_files.status_enabled_success', { name })
            : t('auth_files.status_disabled_success', { name }),
          'success'
        );
      } catch (err: unknown) {
        const errorMessage = err instanceof Error ? err.message : '';
        setFiles((prev) =>
          prev.map((f) => (f.name === name ? { ...f, disabled: previousDisabled } : f))
        );
        showNotification(`${t('notification.update_failed')}: ${errorMessage}`, 'error');
      } finally {
        setStatusUpdating((prev) => {
          if (!prev[name]) return prev;
          const next = { ...prev };
          delete next[name];
          return next;
        });
      }
    },
    [clearInspectionOwnershipForFile, showNotification, t]
  );

  const batchSetStatus = useCallback(
    async (names: string[], enabled: boolean) => {
      if (batchStatusPendingRef.current) return;

      const uniqueNames = Array.from(new Set(names));
      if (uniqueNames.length === 0) return;
      if (uniqueNames.some((name) => statusUpdating[name] === true)) return;

      const originalDisabled = new Map(
        files
          .filter((file) => uniqueNames.includes(file.name))
          .map((file) => [file.name, file.disabled === true])
      );
      const targetNames = new Set(originalDisabled.keys());
      const targetNameList = Array.from(targetNames);
      if (targetNameList.length === 0) return;

      const nextDisabled = !enabled;

      batchStatusPendingRef.current = true;
      setBatchStatusUpdating(true);
      setStatusUpdating((prev) => {
        const next = { ...prev };
        targetNameList.forEach((name) => {
          next[name] = true;
        });
        return next;
      });
      setFiles((prev) =>
        prev.map((file) =>
          targetNames.has(file.name) ? { ...file, disabled: nextDisabled } : file
        )
      );

      try {
        const results = await Promise.allSettled(
          targetNameList.map((name) => authFilesApi.setStatus(name, nextDisabled))
        );

        let successCount = 0;
        let failCount = 0;
        const failedNames = new Set<string>();
        const confirmedDisabled = new Map<string, boolean>();

        results.forEach((result, index) => {
          const name = targetNameList[index];
          if (result.status === 'fulfilled') {
            successCount++;
            confirmedDisabled.set(name, result.value.disabled);
            clearInspectionOwnershipForFile(name);
          } else {
            failCount++;
            failedNames.add(name);
          }
        });

        setFiles((prev) =>
          prev.map((file) => {
            if (failedNames.has(file.name)) {
              return { ...file, disabled: originalDisabled.get(file.name) === true };
            }
            if (confirmedDisabled.has(file.name)) {
              return { ...file, disabled: confirmedDisabled.get(file.name) };
            }
            return file;
          })
        );

        if (failCount === 0) {
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
      } finally {
        batchStatusPendingRef.current = false;
        setBatchStatusUpdating(false);
        setStatusUpdating((prev) => {
          const next = { ...prev };
          targetNameList.forEach((name) => {
            delete next[name];
          });
          return next;
        });
      }
    },
    [clearInspectionOwnershipForFile, deselectAll, files, showNotification, statusUpdating, t]
  );

  const batchPatchFields = useCallback(
    async (
      targets: AuthFilePatchTarget[],
      fields: AuthFileFieldsPatch
    ): Promise<AuthFilesBatchPatchResult | null> => {
      if (batchFieldsPendingRef.current) return null;

      const normalizedTargets = normalizeBatchPatchTargets(targets);
      if (normalizedTargets.length === 0) return null;
      if (Object.keys(fields).length === 0) return null;

      const groups = groupBatchPatchTargets(normalizedTargets);
      batchFieldsPendingRef.current = true;
      setBatchFieldsUpdating(true);

      try {
        const results = await Promise.allSettled(
          groups.map((group) => {
            if (group.authIndexes.length > 0 && group.authIndexes.length === group.targets.length) {
              return authFilesApi.patchFieldsForAuthIndexes(group.name, group.authIndexes, fields);
            }
            return authFilesApi.patchFields(group.name, fields);
          })
        );

        let success = 0;
        let failed = 0;
        const failedNames: string[] = [];

        results.forEach((result, index) => {
          const group = groups[index];
          if (result.status === 'fulfilled') {
            success += group.targets.length;
            return;
          }
          failed += group.targets.length;
          failedNames.push(group.name);
        });

        if (success > 0) {
          try {
            await loadFiles({ throwOnError: true });
          } catch (err: unknown) {
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
        return { success, failed, failedNames };
      } finally {
        batchFieldsPendingRef.current = false;
        setBatchFieldsUpdating(false);
      }
    },
    [deselectAll, loadFiles, showNotification, t]
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
    (names: string[]) => {
      const uniqueNames = Array.from(new Set(names));
      if (uniqueNames.length === 0) return;

      showConfirmation({
        title: t('auth_files.batch_delete_title'),
        message: t('auth_files.batch_delete_confirm', { count: uniqueNames.length }),
        variant: 'danger',
        confirmText: t('common.confirm'),
        onConfirm: async () => {
          try {
            const result = await authFilesApi.deleteFiles(uniqueNames);
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
    [applyDeletedFiles, showConfirmation, showNotification, t]
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
