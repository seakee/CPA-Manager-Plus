import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  authFilesApi,
  applyAuthFileFieldsPatchToRecord,
  type AuthFileLookupTarget,
  type AuthFilesApiRequestScope,
} from '@/services/api';
import type { AuthFileItem } from '@/types';
import { useNotificationStore } from '@/stores';
import {
  AUTH_FILE_CONFIGURATION_INVALID_JSON,
  AUTH_FILE_CONFIGURATION_TARGET_NOT_FOUND,
  buildAuthFileConfigurationDraft,
  buildAuthFileConfigurationPatch,
  buildRedactedAuthFileConfigurationText,
  parseAuthFileConfigurationSource,
  type AuthFileConfigurationDraft,
  type AuthFileConfigurationErrors,
} from '@/features/authFiles/model/authFileConfiguration';
import { lookupAuthFileMutationSnapshot } from '@/features/authFiles/model/authFileMutationSnapshot';
import {
  getAuthFilePatchTarget,
  getAuthFileSelectionKey,
} from '@/features/authFiles/model/credentialStatus';
import {
  readAuthFileStatusPhysicalName,
  resolveAuthFileStatusMutationTarget,
} from '@/utils/authFileStatusMutation';

export type AuthFileConfigurationEditorState = {
  authFile: AuthFileItem;
  fileName: string;
  loading: boolean;
  saving: boolean;
  error: string;
  record: Record<string, unknown> | null;
  recordIndex: number | null;
  providerKey: string;
  originalDraft: AuthFileConfigurationDraft | null;
  draft: AuthFileConfigurationDraft | null;
};

export type UseAuthFileConfigurationEditorOptions = {
  file: AuthFileItem | null;
  enabled: boolean;
  disableControls: boolean;
  sourceMemberCount?: number;
  connectionKey?: string | null;
  requestScope?: AuthFilesApiRequestScope;
  reconcileSource?: (physicalName: string) => Promise<void>;
  onSaved?: (fileName: string) => void;
};

export type UseAuthFileConfigurationEditorResult = {
  state: AuthFileConfigurationEditorState | null;
  draft: AuthFileConfigurationDraft | null;
  errors: AuthFileConfigurationErrors;
  dirty: boolean;
  canSave: boolean;
  rawDataText: string;
  sourceMemberCount: number;
  sharedSourceReadOnly: boolean;
  updateField: <K extends keyof AuthFileConfigurationDraft>(
    field: K,
    value: AuthFileConfigurationDraft[K]
  ) => void;
  reset: () => void;
  reload: () => Promise<void>;
  save: () => Promise<void>;
};

const EMPTY_ERRORS: AuthFileConfigurationErrors = {};

const hasLegacyExcludedModelsAlias = (record: Record<string, unknown>): boolean =>
  Object.prototype.hasOwnProperty.call(record, 'excluded_models') ||
  Object.prototype.hasOwnProperty.call(record, 'excludedModels');

type ScopedConfigurationEditorState = {
  scopeKey: string;
  value: AuthFileConfigurationEditorState;
};

const getConfigurationScopeKey = (connectionKey: string, file: AuthFileItem | null): string =>
  `${connectionKey}\u0000${file ? getAuthFileSelectionKey(file) : ''}`;

export function useAuthFileConfigurationEditor(
  options: UseAuthFileConfigurationEditorOptions
): UseAuthFileConfigurationEditorResult {
  const {
    file,
    enabled,
    disableControls,
    sourceMemberCount = 0,
    connectionKey = '',
    requestScope,
    reconcileSource,
    onSaved,
  } = options;
  const { t } = useTranslation();
  const showNotification = useNotificationStore((store) => store.showNotification);
  const fileRef = useRef(file);
  fileRef.current = file;
  const normalizedConnectionKey = String(connectionKey ?? '');
  const connectionKeyRef = useRef(normalizedConnectionKey);
  connectionKeyRef.current = normalizedConnectionKey;
  const requestIdRef = useRef(0);
  const saveInFlightScopesRef = useRef<Set<string>>(new Set());
  const fileIdentity = file ? getAuthFileSelectionKey(file) : '';
  const currentScopeKey = getConfigurationScopeKey(normalizedConnectionKey, file);
  const [scopedState, setScopedState] = useState<ScopedConfigurationEditorState | null>(null);
  const state = scopedState?.scopeKey === currentScopeKey ? scopedState.value : null;
  const normalizedSourceMemberCount = Number.isFinite(sourceMemberCount)
    ? Math.max(0, Math.trunc(sourceMemberCount))
    : 0;
  const sharedSourceReadOnly = Boolean(
    state?.record && state.recordIndex === null && normalizedSourceMemberCount > 1
  );

  const resolveLoadError = useCallback(
    (error: unknown): string => {
      if (error instanceof Error) {
        if (error.message === AUTH_FILE_CONFIGURATION_INVALID_JSON) {
          return t('accounts.config_error_invalid_source');
        }
        if (error.message === AUTH_FILE_CONFIGURATION_TARGET_NOT_FOUND) {
          return t('accounts.config_error_target_not_found');
        }
        if (error.message.trim()) return error.message;
      }
      return t('notification.download_failed');
    },
    [t]
  );

  const load = useCallback(async () => {
    const targetFile = fileRef.current;
    if (
      !enabled ||
      !targetFile ||
      targetFile.runtimeOnly === true ||
      targetFile.runtime_only === true
    ) {
      setScopedState(null);
      return;
    }

    const requestId = requestIdRef.current + 1;
    requestIdRef.current = requestId;
    const requestConnectionKey = normalizedConnectionKey;
    const fileName = targetFile.name;
    const selectionKey = getAuthFileSelectionKey(targetFile);
    const scopeKey = getConfigurationScopeKey(requestConnectionKey, targetFile);
    setScopedState((previous) =>
      previous?.scopeKey === scopeKey && previous.value.record
        ? { ...previous, value: { ...previous.value, loading: true, error: '' } }
        : {
            scopeKey,
            value: {
              authFile: targetFile,
              fileName,
              loading: true,
              saving: false,
              error: '',
              record: null,
              recordIndex: null,
              providerKey: '',
              originalDraft: null,
              draft: null,
            },
          }
    );

    try {
      const rawText = requestScope
        ? await authFilesApi.downloadText(fileName, requestScope)
        : await authFilesApi.downloadText(fileName);
      if (requestIdRef.current !== requestId || connectionKeyRef.current !== requestConnectionKey) {
        return;
      }
      const parsed = parseAuthFileConfigurationSource(rawText, targetFile);
      const draft = buildAuthFileConfigurationDraft(parsed.record, parsed.providerKey);
      setScopedState({
        scopeKey,
        value: {
          authFile: targetFile,
          fileName,
          loading: false,
          saving: false,
          error: '',
          record: parsed.record,
          recordIndex: parsed.recordIndex,
          providerKey: parsed.providerKey,
          originalDraft: draft,
          draft,
        },
      });
    } catch (error: unknown) {
      if (requestIdRef.current !== requestId || connectionKeyRef.current !== requestConnectionKey) {
        return;
      }
      const message = resolveLoadError(error);
      setScopedState((previous) =>
        previous?.scopeKey === scopeKey &&
        getAuthFileSelectionKey(previous.value.authFile) === selectionKey
          ? { ...previous, value: { ...previous.value, loading: false, error: message } }
          : previous
      );
    }
  }, [enabled, normalizedConnectionKey, requestScope, resolveLoadError]);

  useEffect(() => {
    let cancelled = false;
    queueMicrotask(() => {
      if (!cancelled) void load();
    });
    return () => {
      cancelled = true;
      requestIdRef.current += 1;
    };
  }, [fileIdentity, load, normalizedConnectionKey]);

  const patchResult = useMemo(() => {
    if (!state?.record || !state.originalDraft || !state.draft) {
      return { patch: {}, errors: EMPTY_ERRORS };
    }
    return buildAuthFileConfigurationPatch(
      state.record,
      state.providerKey,
      state.originalDraft,
      state.draft
    );
  }, [state]);

  const hasPatch = Object.keys(patchResult.patch).length > 0;
  const hasErrors = Object.keys(patchResult.errors).length > 0;
  // Dirty state follows the normalized mutation result. Formatting-only edits
  // (for example, JSON whitespace or surrounding spaces) must not block
  // navigation when they would produce no server mutation. Invalid edits do
  // remain dirty so the unsaved-changes guard cannot silently discard them.
  const dirty = Boolean(state?.record && (hasPatch || hasErrors));
  const canSave =
    Boolean(state?.record) &&
    dirty &&
    hasPatch &&
    !hasErrors &&
    !disableControls &&
    !sharedSourceReadOnly &&
    !state?.loading &&
    !state?.error &&
    state?.saving !== true;
  const rawDataText = state?.record ? buildRedactedAuthFileConfigurationText(state.record) : '';

  const updateField = useCallback(
    <K extends keyof AuthFileConfigurationDraft>(
      field: K,
      value: AuthFileConfigurationDraft[K]
    ) => {
      setScopedState((previous) => {
        if (previous?.scopeKey !== currentScopeKey) return previous;
        if (
          sharedSourceReadOnly ||
          !previous.value.draft ||
          previous.value.saving ||
          previous.value.error ||
          previous.value.loading
        )
          return previous;
        return {
          ...previous,
          value: {
            ...previous.value,
            draft: { ...previous.value.draft, [field]: value },
          },
        };
      });
    },
    [currentScopeKey, sharedSourceReadOnly]
  );

  const reset = useCallback(() => {
    setScopedState((previous) => {
      if (previous?.scopeKey !== currentScopeKey) return previous;
      if (!previous.value.originalDraft || previous.value.saving) return previous;
      return {
        ...previous,
        value: { ...previous.value, draft: previous.value.originalDraft },
      };
    });
  }, [currentScopeKey]);

  const save = useCallback(async () => {
    if (!state?.record || !state.originalDraft || !state.draft || !canSave) return;
    const targetSnapshot = state.authFile;
    const fileName = state.fileName;
    const selectionKey = getAuthFileSelectionKey(targetSnapshot);
    const scopeKey = currentScopeKey;
    if (saveInFlightScopesRef.current.has(scopeKey)) return;
    saveInFlightScopesRef.current.add(scopeKey);
    const requestIdSnapshot = requestIdRef.current;
    const providerKeySnapshot = state.providerKey;
    const recordSnapshot = state.record;
    const patch = patchResult.patch;
    const isCurrentTarget = () => {
      const currentFile = fileRef.current;
      return Boolean(
        requestIdRef.current === requestIdSnapshot &&
        connectionKeyRef.current === normalizedConnectionKey &&
        currentFile &&
        getConfigurationScopeKey(connectionKeyRef.current, currentFile) === scopeKey
      );
    };

    setScopedState((previous) =>
      previous?.scopeKey === scopeKey &&
      getAuthFileSelectionKey(previous.value.authFile) === selectionKey
        ? { ...previous, value: { ...previous.value, saving: true } }
        : previous
    );

    try {
      const lookupFiles = (target: AuthFileLookupTarget) =>
        requestScope ? authFilesApi.lookup(target, requestScope) : authFilesApi.lookup(target);

      const currentFiles = await lookupAuthFileMutationSnapshot(
        getAuthFilePatchTarget(targetSnapshot),
        lookupFiles
      );
      if (!isCurrentTarget()) return;
      const resolution = resolveAuthFileStatusMutationTarget(
        currentFiles,
        getAuthFilePatchTarget(targetSnapshot)
      );
      if (
        !resolution.target ||
        resolution.failure !== null ||
        (resolution.scope !== 'credential' && resolution.scope !== 'expanded-child')
      ) {
        throw new Error(t('auth_files.status_mutation_scope_ambiguous', { name: fileName }));
      }

      const patchTarget = getAuthFilePatchTarget(resolution.target);
      const targetPhysicalName = readAuthFileStatusPhysicalName(resolution.target);
      const sourceIdentities = currentFiles
        .filter((entry) => readAuthFileStatusPhysicalName(entry) === targetPhysicalName)
        .map(getAuthFilePatchTarget);
      if (patch['excluded-models'] !== undefined && hasLegacyExcludedModelsAlias(recordSnapshot)) {
        // CPA currently reads `excluded_models` before `excluded-models` and
        // treats PATCH null values as retained metadata keys. Rewrite the
        // verified source JSON so the legacy key is actually removed; this
        // prevents a stale legacy list from shadowing the canonical one at
        // runtime. The helper performs identity and content-hash checks.
        if (requestScope) {
          await authFilesApi.patchFieldsForAuthIndexes(
            resolution.target.name,
            [patchTarget],
            sourceIdentities,
            patch,
            requestScope
          );
        } else {
          await authFilesApi.patchFieldsForAuthIndexes(
            resolution.target.name,
            [patchTarget],
            sourceIdentities,
            patch
          );
        }
      } else {
        if (requestScope) {
          await authFilesApi.patchFieldsWithPluginSourceFallback(
            patchTarget,
            patch,
            sourceIdentities,
            requestScope
          );
        } else {
          await authFilesApi.patchFieldsWithPluginSourceFallback(
            patchTarget,
            patch,
            sourceIdentities
          );
        }
      }
      if (!isCurrentTarget()) return;

      let nextRecord = applyAuthFileFieldsPatchToRecord(recordSnapshot, patch);
      let nextProviderKey = providerKeySnapshot;
      let nextRecordIndex = state.recordIndex;
      let sourceRefreshWarning = '';
      try {
        const refreshedRawText = requestScope
          ? await authFilesApi.downloadText(fileName, requestScope)
          : await authFilesApi.downloadText(fileName);
        if (!isCurrentTarget()) return;
        const refreshed = parseAuthFileConfigurationSource(
          refreshedRawText,
          resolution.target ?? targetSnapshot
        );
        nextRecord = refreshed.record;
        nextProviderKey = refreshed.providerKey;
        nextRecordIndex = refreshed.recordIndex;
      } catch (refreshError: unknown) {
        if (!isCurrentTarget()) return;
        sourceRefreshWarning = resolveLoadError(refreshError);
      }
      const normalizedDraft = buildAuthFileConfigurationDraft(nextRecord, nextProviderKey);
      setScopedState((previous) => {
        if (
          previous?.scopeKey !== scopeKey ||
          getAuthFileSelectionKey(previous.value.authFile) !== selectionKey
        ) {
          return previous;
        }
        return {
          ...previous,
          value: {
            ...previous.value,
            authFile: resolution.target ?? previous.value.authFile,
            saving: false,
            error: sourceRefreshWarning,
            record: nextRecord,
            recordIndex: nextRecordIndex,
            providerKey: nextProviderKey,
            originalDraft: normalizedDraft,
            draft: normalizedDraft,
          },
        };
      });
      showNotification(t('accounts.config_saved_success'), 'success');
      if (sourceRefreshWarning) {
        showNotification(
          `${t('notification.download_failed')}: ${sourceRefreshWarning}`,
          'warning'
        );
      }
      if (reconcileSource) {
        try {
          await reconcileSource(targetPhysicalName || fileName);
        } catch (refreshError: unknown) {
          if (isCurrentTarget()) {
            const refreshMessage =
              refreshError instanceof Error ? refreshError.message : t('common.unknown_error');
            showNotification(`${t('notification.load_failed')}: ${refreshMessage}`, 'warning');
          }
        }
      }
      if (isCurrentTarget()) onSaved?.(fileName);
    } catch (error: unknown) {
      if (!isCurrentTarget()) return;
      const message = error instanceof Error ? error.message : t('common.unknown_error');
      showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
      setScopedState((previous) =>
        previous?.scopeKey === scopeKey &&
        getAuthFileSelectionKey(previous.value.authFile) === selectionKey
          ? { ...previous, value: { ...previous.value, saving: false } }
          : previous
      );
    } finally {
      saveInFlightScopesRef.current.delete(scopeKey);
    }
  }, [
    canSave,
    currentScopeKey,
    normalizedConnectionKey,
    onSaved,
    patchResult.patch,
    reconcileSource,
    requestScope,
    resolveLoadError,
    showNotification,
    state,
    t,
  ]);

  return {
    state,
    draft: state?.draft ?? null,
    errors: patchResult.errors,
    dirty,
    canSave,
    rawDataText,
    sourceMemberCount: normalizedSourceMemberCount,
    sharedSourceReadOnly,
    updateField,
    reset,
    reload: load,
    save,
  };
}
