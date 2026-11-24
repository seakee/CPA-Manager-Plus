import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { publishAccountCredentialMutationRevision, useNotificationStore } from '@/stores';
import type { ApiClientRequestScope } from '@/services/api/client';
import { vertexApi, type VertexImportResponse } from '@/services/api/vertex';
import { recordAccountCredentialMutationMarker } from '@/features/accounts/model/accountCredentialMutationMarker';

export interface VertexImportResult {
  projectId?: string;
  email?: string;
  location?: string;
  authFile?: string;
}

export interface VertexImportState {
  file?: File;
  fileName: string;
  location: string;
  loading: boolean;
  error?: string;
  result?: VertexImportResult;
}

interface UseVertexImportOptions {
  connectionFingerprint: string | null;
  requestScope: ApiClientRequestScope;
  onCredentialAdded?: () => void;
}

function getErrorMessage(error: unknown): string {
  if (error instanceof Error) return error.message;
  if (typeof error === 'object' && error !== null && 'message' in error) {
    if (typeof error.message === 'string') return error.message;
  }
  return typeof error === 'string' ? error : '';
}

const INITIAL_VERTEX_STATE: VertexImportState = {
  fileName: '',
  location: '',
  loading: false,
};

export function useVertexImport({
  connectionFingerprint,
  requestScope,
  onCredentialAdded,
}: UseVertexImportOptions) {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const [vertexEntry, setVertexEntry] = useState<{
    fingerprint: string | null;
    state: VertexImportState;
  }>(() => ({
    fingerprint: connectionFingerprint,
    state: INITIAL_VERTEX_STATE,
  }));
  const vertex =
    vertexEntry.fingerprint === connectionFingerprint ? vertexEntry.state : INITIAL_VERTEX_STATE;
  const updateVertex = useCallback(
    (updater: (prev: VertexImportState) => VertexImportState) => {
      setVertexEntry((prev) => {
        const base =
          prev.fingerprint === connectionFingerprint ? prev.state : INITIAL_VERTEX_STATE;
        return {
          fingerprint: connectionFingerprint,
          state: updater(base),
        };
      });
    },
    [connectionFingerprint]
  );
  const connectionFingerprintRef = useRef(connectionFingerprint);
  const generationRef = useRef(0);
  const abortControllerRef = useRef<AbortController | null>(null);

  useLayoutEffect(() => {
    const previousFingerprint = connectionFingerprintRef.current;
    connectionFingerprintRef.current = connectionFingerprint;
    if (previousFingerprint === connectionFingerprint) return;
    generationRef.current += 1;
    abortControllerRef.current?.abort();
    abortControllerRef.current = null;
  }, [connectionFingerprint]);
  useEffect(() => {
    return () => {
      generationRef.current += 1;
      abortControllerRef.current?.abort();
      abortControllerRef.current = null;
    };
  }, []);

  const pickFile = useCallback(
    (file: File | undefined) => {
      if (!file) return;
      if (!file.name.toLowerCase().endsWith('.json')) {
        const message = t('vertex_import.file_required');
        updateVertex((prev) => ({ ...prev, error: message }));
        showNotification(message, 'warning');
        return;
      }
      updateVertex((prev) => ({
        ...prev,
        file,
        fileName: file.name,
        error: undefined,
        result: undefined,
      }));
    },
    [showNotification, t, updateVertex]
  );

  const clearFile = useCallback(() => {
    updateVertex((prev) => ({
      ...prev,
      file: undefined,
      fileName: '',
      error: undefined,
    }));
  }, [updateVertex]);

  const setLocation = useCallback(
    (location: string) => {
      updateVertex((prev) => ({ ...prev, location }));
    },
    [updateVertex]
  );

  const resetForAnother = useCallback(() => {
    updateVertex((prev) => ({
      file: undefined,
      fileName: '',
      location: prev.location,
      loading: false,
      error: undefined,
      result: undefined,
    }));
  }, [updateVertex]);

  const importCredential = useCallback(async () => {
    if (!vertex.file) {
      const message = t('vertex_import.file_required');
      updateVertex((prev) => ({ ...prev, error: message }));
      showNotification(message, 'warning');
      return;
    }
    const location = vertex.location.trim();
    const importConnectionFingerprint = connectionFingerprint;
    if (!importConnectionFingerprint) return;

    const generation = generationRef.current + 1;
    generationRef.current = generation;
    abortControllerRef.current?.abort();
    const controller = new AbortController();
    abortControllerRef.current = controller;

    const isCurrentImport = () =>
      connectionFingerprintRef.current === importConnectionFingerprint &&
      generationRef.current === generation;

    updateVertex((prev) => ({ ...prev, loading: true, error: undefined, result: undefined }));
    try {
      const res: VertexImportResponse = await vertexApi.importCredential(
        vertex.file,
        location || undefined,
        requestScope,
        controller.signal
      );
      if (!isCurrentImport()) return;
      const result: VertexImportResult = {
        projectId: res.project_id,
        email: res.email,
        location: res.location,
        authFile: res['auth-file'] ?? res.auth_file,
      };
      recordAccountCredentialMutationMarker({
        connectionFingerprint: importConnectionFingerprint,
        provider: 'vertex',
      });
      publishAccountCredentialMutationRevision({
        connectionFingerprint: importConnectionFingerprint,
        provider: 'vertex',
        kind: 'credential',
      });
      onCredentialAdded?.();
      updateVertex((prev) => ({ ...prev, loading: false, result }));
      showNotification(t('vertex_import.success'), 'success');
    } catch (err: unknown) {
      if (!isCurrentImport()) return;
      const message = getErrorMessage(err);
      updateVertex((prev) => ({
        ...prev,
        loading: false,
        error: message || t('notification.upload_failed'),
      }));
      const notification = message
        ? `${t('notification.upload_failed')}: ${message}`
        : t('notification.upload_failed');
      showNotification(notification, 'error');
    }
  }, [
    connectionFingerprint,
    onCredentialAdded,
    requestScope,
    showNotification,
    t,
    updateVertex,
    vertex.file,
    vertex.location,
  ]);
  return {
    vertex,
    pickFile,
    clearFile,
    setLocation,
    importCredential,
    resetForAnother,
  };
}
