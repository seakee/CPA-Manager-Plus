import { useEffect, useMemo, useState } from 'react';
import {
  isUsageServiceId,
  normalizeUsageServiceBase,
  usageServiceApi,
  type ManagerConfig,
} from '@/services/api/usageService';
import { DEMO_API_BASE, isDemoMode } from '@/features/demo/demoMode';
import { useAuthStore } from '@/stores';
import { useUsageServiceStore } from '@/stores/useUsageServiceStore';
import { detectApiBaseFromLocation } from '@/utils/connection';

export type PanelHostMode = 'manager_embedded' | 'external_panel';

export type PanelFeatureUnavailableReason =
  | 'checking'
  | 'service_not_configured'
  | 'service_unavailable'
  | 'monitoring_disabled';

export interface PanelFeatureAvailability {
  checking: boolean;
  panelHostConfirmed: boolean;
  panelHostMode: PanelHostMode;
  panelBase: string;
  managerServiceBase: string;
  managerServiceAvailable: boolean;
  requestMonitoringAvailable: boolean;
  modelPricesAvailable: boolean;
  serverCodexInspectionAvailable: boolean;
  dockerSetupAvailable: boolean;
  externalManagerConfigAvailable: boolean;
  reason: PanelFeatureUnavailableReason | '';
}

export interface ResolvePanelFeatureAvailabilityInput {
  checking?: boolean;
  panelHostConfirmed: boolean;
  panelHostedByUsageService: boolean;
  panelBase: string;
  managerServiceBase: string;
  managerConfig: ManagerConfig | null;
  hasManagerCandidate: boolean;
  managementKey: string;
}

const normalizeBase = (value?: string) => normalizeUsageServiceBase(value || '');

const buildUnavailableState = (
  input: ResolvePanelFeatureAvailabilityInput,
  reason: PanelFeatureUnavailableReason
): PanelFeatureAvailability => ({
  checking: input.checking === true,
  panelHostConfirmed: input.panelHostConfirmed,
  panelHostMode: input.panelHostedByUsageService ? 'manager_embedded' : 'external_panel',
  panelBase: normalizeBase(input.panelBase),
  managerServiceBase: '',
  managerServiceAvailable: false,
  requestMonitoringAvailable: false,
  modelPricesAvailable: false,
  serverCodexInspectionAvailable: false,
  dockerSetupAvailable: input.panelHostedByUsageService,
  externalManagerConfigAvailable: false,
  reason,
});

export function resolvePanelFeatureAvailability(
  input: ResolvePanelFeatureAvailabilityInput
): PanelFeatureAvailability {
  if (!input.managementKey) {
    return buildUnavailableState(input, 'service_not_configured');
  }
  if (!input.panelHostConfirmed) {
    return buildUnavailableState(input, 'service_unavailable');
  }
  if (!input.panelHostedByUsageService) {
    return buildUnavailableState(input, 'service_not_configured');
  }

  const managerServiceBase = normalizeBase(input.managerServiceBase);
  if (!managerServiceBase || !input.managerConfig) {
    return buildUnavailableState(
      input,
      input.hasManagerCandidate ? 'service_unavailable' : 'service_not_configured'
    );
  }

  const hasCPAConnection = Boolean(
    input.managerConfig.cpaConnection?.cpaBaseUrl &&
    (input.managerConfig.cpaConnection?.managementKeyConfigured ||
      input.managerConfig.cpaConnection?.managementKey)
  );
  const collectorEnabled = input.managerConfig.collector?.enabled !== false;
  const requestMonitoringAvailable = hasCPAConnection && collectorEnabled;

  return {
    checking: input.checking === true,
    panelHostConfirmed: input.panelHostConfirmed,
    panelHostMode: input.panelHostedByUsageService ? 'manager_embedded' : 'external_panel',
    panelBase: normalizeBase(input.panelBase),
    managerServiceBase,
    managerServiceAvailable: true,
    requestMonitoringAvailable,
    modelPricesAvailable: true,
    serverCodexInspectionAvailable: hasCPAConnection,
    dockerSetupAvailable: input.panelHostedByUsageService,
    externalManagerConfigAvailable: false,
    reason: requestMonitoringAvailable
      ? ''
      : !hasCPAConnection
        ? 'service_not_configured'
        : 'monitoring_disabled',
  };
}

export interface BuildPanelManagerServiceCandidatesInput {
  panelHostConfirmed: boolean;
  panelHostedByUsageService: boolean;
  panelBase: string;
}

export function buildPanelManagerServiceCandidates({
  panelHostConfirmed,
  panelHostedByUsageService,
  panelBase,
}: BuildPanelManagerServiceCandidatesInput): string[] {
  const normalizedPanelBase = normalizeBase(panelBase);
  if (panelHostConfirmed && panelHostedByUsageService) {
    return normalizedPanelBase ? [normalizedPanelBase] : [];
  }

  return [];
}

export function managerConfigMatchesPanel({
  panelHostedByUsageService,
}: {
  panelHostedByUsageService: boolean;
  apiBase: string;
  config: ManagerConfig;
}): boolean {
  return panelHostedByUsageService;
}

type PanelFeatureAvailabilityRequestInput = {
  apiBase: string;
  managementKey: string;
  usageServiceRevision: number;
  panelBase: string;
};

type PanelFeatureAvailabilityRequest = {
  key: string;
  promise: Promise<PanelFeatureAvailability>;
};

const initialAvailability: PanelFeatureAvailability = {
  checking: true,
  panelHostConfirmed: false,
  panelHostMode: 'external_panel',
  panelBase: '',
  managerServiceBase: '',
  managerServiceAvailable: false,
  requestMonitoringAvailable: false,
  modelPricesAvailable: false,
  serverCodexInspectionAvailable: false,
  dockerSetupAvailable: false,
  externalManagerConfigAvailable: false,
  reason: 'checking',
};

const demoAvailability: PanelFeatureAvailability = {
  checking: false,
  panelHostConfirmed: true,
  panelHostMode: 'manager_embedded',
  panelBase: DEMO_API_BASE,
  managerServiceBase: DEMO_API_BASE,
  managerServiceAvailable: true,
  requestMonitoringAvailable: true,
  modelPricesAvailable: true,
  serverCodexInspectionAvailable: true,
  dockerSetupAvailable: true,
  externalManagerConfigAvailable: false,
  reason: '',
};

let cachedAvailabilityKey = '';
let cachedAvailability: PanelFeatureAvailability | null = null;
let inFlightAvailabilityRequest: PanelFeatureAvailabilityRequest | null = null;
let latestAvailabilityRequestKey = '';

const buildAvailabilityRequestKey = ({
  apiBase,
  managementKey,
  usageServiceRevision,
  panelBase,
}: PanelFeatureAvailabilityRequestInput): string =>
  [
    normalizeBase(panelBase),
    normalizeBase(apiBase),
    managementKey,
    String(usageServiceRevision),
  ].join('\u001f');

const isConfirmedExternalPanelProbeFailure = (error: unknown): boolean =>
  error !== null && typeof error === 'object' && (error as { status?: unknown }).status === 404;

async function detectPanelFeatureAvailability({
  apiBase,
  managementKey,
  panelBase,
}: PanelFeatureAvailabilityRequestInput): Promise<PanelFeatureAvailability> {
  const normalizedPanelBase = normalizeBase(panelBase);
  if (!managementKey) {
    return resolvePanelFeatureAvailability({
      checking: false,
      panelHostConfirmed: false,
      panelHostedByUsageService: false,
      panelBase: normalizedPanelBase,
      managerServiceBase: '',
      managerConfig: null,
      hasManagerCandidate: false,
      managementKey,
    });
  }

  let panelHostedByUsageService = false;
  let panelHostConfirmed = false;
  try {
    const info = await usageServiceApi.getInfo(normalizedPanelBase);
    panelHostConfirmed = true;
    panelHostedByUsageService = isUsageServiceId(info.service);
  } catch (error) {
    panelHostConfirmed = isConfirmedExternalPanelProbeFailure(error);
    panelHostedByUsageService = false;
  }

  const candidates = buildPanelManagerServiceCandidates({
    panelHostConfirmed,
    panelHostedByUsageService,
    panelBase: normalizedPanelBase,
  });

  for (const candidate of candidates) {
    try {
      const info = await usageServiceApi.getInfo(candidate);
      if (!isUsageServiceId(info.service)) continue;
      const response = await usageServiceApi.getManagerConfig(candidate, managementKey);
      if (
        !managerConfigMatchesPanel({
          panelHostedByUsageService,
          apiBase,
          config: response.config,
        })
      ) {
        continue;
      }
      return resolvePanelFeatureAvailability({
        checking: false,
        panelHostConfirmed,
        panelHostedByUsageService,
        panelBase: normalizedPanelBase,
        managerServiceBase: candidate,
        managerConfig: response.config,
        hasManagerCandidate: candidates.length > 0,
        managementKey,
      });
    } catch {
      // Continue probing; a regular CPA endpoint or unreachable Manager Server is expected here.
    }
  }

  const unavailableState = resolvePanelFeatureAvailability({
    checking: false,
    panelHostConfirmed,
    panelHostedByUsageService,
    panelBase: normalizedPanelBase,
    managerServiceBase: '',
    managerConfig: null,
    hasManagerCandidate: candidates.length > 0,
    managementKey,
  });
  return unavailableState;
}

function requestPanelFeatureAvailability(input: PanelFeatureAvailabilityRequestInput): {
  key: string;
  promise: Promise<PanelFeatureAvailability>;
} {
  const key = buildAvailabilityRequestKey(input);
  if (cachedAvailabilityKey === key && cachedAvailability) {
    return { key, promise: Promise.resolve(cachedAvailability) };
  }
  if (inFlightAvailabilityRequest?.key === key) {
    return inFlightAvailabilityRequest;
  }

  latestAvailabilityRequestKey = key;
  const promise = detectPanelFeatureAvailability(input).then((availability) => {
    if (latestAvailabilityRequestKey === key) {
      cachedAvailabilityKey = key;
      cachedAvailability = availability;
    }
    return availability;
  });
  inFlightAvailabilityRequest = { key, promise };
  promise.finally(() => {
    if (inFlightAvailabilityRequest?.key === key) {
      inFlightAvailabilityRequest = null;
    }
  });
  return inFlightAvailabilityRequest;
}

export function usePanelFeatureAvailability(): PanelFeatureAvailability {
  const demoMode = __DEMO_SITE__ && isDemoMode();
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey);
  const usageServiceRevision = useUsageServiceStore((state) => state.revision);
  const panelBase = useMemo(() => detectApiBaseFromLocation(), []);
  const requestInput = useMemo(
    () => ({
      apiBase,
      managementKey,
      usageServiceRevision,
      panelBase,
    }),
    [apiBase, managementKey, panelBase, usageServiceRevision]
  );
  const requestKey = useMemo(() => buildAvailabilityRequestKey(requestInput), [requestInput]);
  const [state, setState] = useState<{
    requestKey: string;
    availability: PanelFeatureAvailability;
  }>(() => ({
    requestKey,
    availability: demoMode
      ? demoAvailability
      : cachedAvailabilityKey === requestKey && cachedAvailability
        ? cachedAvailability
        : initialAvailability,
  }));

  useEffect(() => {
    let cancelled = false;
    if (demoMode) {
      return () => {
        cancelled = true;
      };
    }

    const request = requestPanelFeatureAvailability(requestInput);
    request.promise.then((availability) => {
      if (cancelled || request.key !== requestKey) return;
      setState({ requestKey, availability });
    });

    return () => {
      cancelled = true;
    };
  }, [panelBase, demoMode, requestInput, requestKey]);

  if (demoMode) return demoAvailability;
  if (state.requestKey === requestKey) return state.availability;

  return {
    ...initialAvailability,
    panelBase: normalizeBase(panelBase),
  };
}
