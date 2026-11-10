import { useCallback, useEffect, useRef, useState } from 'react';
import {
  refreshPanelFeatureAvailability,
  usePanelFeatureAvailability,
} from '@/hooks/usePanelFeatureAvailability';
import { buildRequestMonitoringConfig } from '@/features/monitoring/requestMonitoringConfig';
import { usageServiceApi } from '@/services/api/usageService';
import { useAuthStore } from '@/stores';

export interface RequestMonitoringSwitch {
  /** Stored `collector.enabled`; only meaningful together with `loaded`. */
  enabled: boolean;
  loaded: boolean;
  saving: boolean;
  failed: boolean;
  /** False when this Console has no Manager service to read or write through. */
  writable: boolean;
  setEnabled: (enabled: boolean) => Promise<void>;
}

/**
 * One write path for the request-monitoring switch: the System page card and
 * the monitoring page's "not started" state both use it, so enabling from
 * either place stores exactly the same thing.
 */
export function useRequestMonitoringSwitch(): RequestMonitoringSwitch {
  const { managerServiceBase, checking } = usePanelFeatureAvailability();
  const managementKey = useAuthStore((state) => state.managementKey);
  const [stored, setStored] = useState<{ serviceBase: string; enabled: boolean }>({
    serviceBase: '',
    enabled: false,
  });
  const [saving, setSaving] = useState(false);
  const [failed, setFailed] = useState(false);
  const writeInFlight = useRef(false);

  useEffect(() => {
    if (!managerServiceBase) return;
    let cancelled = false;
    usageServiceApi.getManagerConfig(managerServiceBase, managementKey).then(
      (response) => {
        if (cancelled) return;
        setStored({
          serviceBase: managerServiceBase,
          enabled: response.config.collector?.enabled !== false,
        });
      },
      () => undefined
    );
    return () => {
      cancelled = true;
    };
  }, [managementKey, managerServiceBase]);

  const setEnabled = useCallback(
    async (next: boolean) => {
      if (!managerServiceBase || writeInFlight.current) return;
      writeInFlight.current = true;
      setSaving(true);
      setFailed(false);
      try {
        const current = await usageServiceApi.getManagerConfig(managerServiceBase, managementKey);
        const response = await usageServiceApi.saveManagerConfig(
          managerServiceBase,
          buildRequestMonitoringConfig(current.config, next),
          managementKey
        );
        setStored({
          serviceBase: managerServiceBase,
          enabled: response.config.collector?.enabled !== false,
        });
        // The sidebar entry and the route gate read the same availability.
        refreshPanelFeatureAvailability();
      } catch {
        setFailed(true);
      } finally {
        writeInFlight.current = false;
        setSaving(false);
      }
    },
    [managementKey, managerServiceBase]
  );

  const loaded = Boolean(managerServiceBase) && stored.serviceBase === managerServiceBase;

  return {
    enabled: loaded && stored.enabled,
    loaded,
    saving,
    failed,
    writable: Boolean(managerServiceBase) && !checking,
    setEnabled,
  };
}
