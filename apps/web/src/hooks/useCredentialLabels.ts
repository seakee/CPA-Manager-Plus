import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useAuthStore } from '@/stores';
import { credentialLabelsApi } from '@/services/api/credentialLabels';

const EMPTY_LABELS: Record<string, string> = {};

export function useCredentialLabels() {
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey);
  const sessionMode = useAuthStore((state) => state.sessionMode);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const scope = useMemo(
    () => ({ apiBase, managementKey, sessionMode, connectionStatus }),
    [apiBase, managementKey, sessionMode, connectionStatus]
  );
  const active = sessionMode === 'manager_embedded' && connectionStatus === 'connected';
  const current = useRef(scope);
  const readID = useRef(0);
  const mutation = useRef<object | null>(null);
  const [snapshot, setSnapshot] = useState<{
    scope: typeof scope;
    labels: Record<string, string>;
    available: boolean;
    ready: boolean;
    error: string;
  }>({ scope, labels: {}, available: false, ready: false, error: '' });
  const [saving, setSaving] = useState(false);
  useLayoutEffect(() => {
    current.current = scope;
    readID.current++;
    mutation.current = null;
    setSaving(false);
    const invalidate = () => {
      readID.current++;
      mutation.current = null;
    };
    return invalidate;
  }, [scope]);

  const refresh = useCallback(async () => {
    if (!active) return;
    const id = ++readID.current;
    try {
      const { items } = await credentialLabelsApi.list(scope);
      if (current.current !== scope || id !== readID.current) return;
      setSnapshot({
        scope,
        labels: Object.fromEntries(items.map((item) => [item.keyHash, item.label])),
        available: true,
        ready: true,
        error: '',
      });
    } catch (error) {
      if (current.current !== scope || id !== readID.current) return;
      const status =
        (error as { status?: number; response?: { status?: number } })?.status ??
        (error as { response?: { status?: number } })?.response?.status;
      if (status === 404 || status === 401 || status === 403) {
        setSnapshot({ scope, labels: {}, available: false, ready: false, error: '' });
      } else {
        setSnapshot((previous) => ({
          scope,
          labels: previous.scope === scope ? previous.labels : {},
          available: previous.scope === scope && previous.available,
          ready: false,
          error: error instanceof Error ? error.message : String(error),
        }));
      }
    }
  }, [scope, active]);
  useEffect(() => {
    void refresh();
    if (typeof window === 'undefined' || typeof window.addEventListener !== 'function') return;
    const changed = () => {
      void refresh();
    };
    window.addEventListener('credential-labels-changed', changed);
    return () => window.removeEventListener('credential-labels-changed', changed);
  }, [refresh]);

  const setLabel = useCallback(
    async (keyHash: string, label: string) => {
      if (!active || !snapshot.ready || snapshot.scope !== scope || mutation.current) return false;
      const operation = {};
      mutation.current = operation;
      readID.current++;
      setSaving(true);
      try {
        const item = await credentialLabelsApi.set(scope, keyHash, label);
        if (current.current !== scope || mutation.current !== operation) return false;
        readID.current++;
        setSnapshot((previous) => {
          const labels = { ...(previous.scope === scope ? previous.labels : {}) };
          if (item.label) labels[item.keyHash] = item.label;
          else delete labels[item.keyHash];
          return { scope, labels, available: true, ready: true, error: '' };
        });
        if (typeof window !== 'undefined' && typeof window.dispatchEvent === 'function')
          window.dispatchEvent(new Event('credential-labels-changed'));
        return true;
      } catch (error) {
        if (current.current !== scope || mutation.current !== operation) return false;
        throw error;
      } finally {
        if (current.current === scope && mutation.current === operation) {
          mutation.current = null;
          setSaving(false);
        }
      }
    },
    [active, snapshot.ready, snapshot.scope, scope]
  );
  const visible = active && snapshot.scope === scope;
  return {
    labels: visible ? snapshot.labels : EMPTY_LABELS,
    available: visible && snapshot.available,
    ready: visible && snapshot.ready,
    error: visible ? snapshot.error : '',
    saving,
    refresh,
    setLabel,
    scope,
  };
}
