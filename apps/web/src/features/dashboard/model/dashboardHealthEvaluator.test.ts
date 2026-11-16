import { describe, expect, it } from 'vitest';
import type { UpdateStatus } from '@/features/system/managerUpdateApi';
import type { UsageServiceStatus } from '@/services/api/usageService';
import {
  evaluateCollectorHealth,
  evaluateDashboardHealthSummary,
  evaluateDashboardVersionUpdates,
  hasCollectorAttention,
  resolveCollectorDeadLetters,
  type EvaluateDashboardHealthSummaryInput,
} from './dashboardHealthEvaluator';

const SCOPE_A = '["http://gw-a:8317","key-a"]';
const SCOPE_B = '["http://gw-b:8317","key-b"]';

const makeBaseInput = (
  overrides: Partial<EvaluateDashboardHealthSummaryInput> = {}
): EvaluateDashboardHealthSummaryInput => ({
  isPanelLite: false,
  panelHostMode: 'manager_embedded',
  managerServiceAvailable: true,
  featureChecking: false,
  featureReason: '',
  usageEnabled: true,
  currentScope: SCOPE_A,
  evidenceScope: SCOPE_A,
  usageEvidenceScope: SCOPE_A,
  status: {
    events: 42,
    deadLetters: 0,
    collector: {
      collector: 'running',
      deadLetters: 0,
    },
  },
  loading: false,
  error: '',
  connectionStatus: 'connected',
  usageLoaded: true,
  usageLoading: false,
  usageError: '',
  errorLogsLoaded: true,
  errorLogsLoading: false,
  errorLogCount: 0,
  errorLogsError: '',
  healthMetaError: '',
  ...overrides,
});

describe('dashboardHealthEvaluator', () => {
  it('T1-2: requires positive collector.collector === "running" confirmation and never treats missing/starting/unknown phases as healthy', () => {
    expect(evaluateDashboardHealthSummary(makeBaseInput({ status: null }))).toMatchObject({
      status: 'loading_unconfirmed',
      collector: {
        state: 'loading_unconfirmed',
        unconfirmedReason: 'status_missing',
      },
    });

    expect(
      evaluateDashboardHealthSummary(makeBaseInput({ status: { events: 5 } }))
    ).toMatchObject({
      status: 'loading_unconfirmed',
      collector: {
        state: 'loading_unconfirmed',
        unconfirmedReason: 'collector_incomplete',
      },
    });

    expect(
      evaluateDashboardHealthSummary(makeBaseInput({ status: { collector: {} } }))
    ).toMatchObject({
      status: 'loading_unconfirmed',
      collector: {
        state: 'loading_unconfirmed',
        unconfirmedReason: 'collector_incomplete',
      },
    });

    expect(
      evaluateDashboardHealthSummary(
        makeBaseInput({ status: { collector: { collector: 'starting' } } })
      )
    ).toMatchObject({
      status: 'loading_unconfirmed',
      collector: {
        state: 'loading_unconfirmed',
        unconfirmedReason: 'collector_starting',
      },
    });

    expect(
      evaluateDashboardHealthSummary(
        makeBaseInput({ status: { collector: { collector: 'reconnecting' } } })
      )
    ).toMatchObject({
      status: 'loading_unconfirmed',
      collector: {
        state: 'loading_unconfirmed',
        unconfirmedReason: 'collector_phase_unknown',
      },
    });

    expect(evaluateDashboardHealthSummary(makeBaseInput())).toMatchObject({
      status: 'confirmed_healthy',
      collector: {
        state: 'confirmed_healthy',
      },
    });
  });

  it('T1-3: preserves confirmed same-scope anomalies during capability re-probe and isolates cross-connection scope switches', () => {
    const reprobeInput = makeBaseInput({
      featureChecking: true,
      usageEnabled: false,
      status: {
        collector: {
          collector: 'running',
          lastError: 'redis timeout',
        },
      },
    });
    expect(evaluateDashboardHealthSummary(reprobeInput)).toMatchObject({
      status: 'attention_required',
      collector: {
        state: 'attention_required',
        reasons: ['collector_last_error'],
        lastError: 'redis timeout',
      },
    });
    expect(hasCollectorAttention(reprobeInput)).toBe(true);

    const switchedScopeInput = makeBaseInput({
      currentScope: SCOPE_B,
      evidenceScope: SCOPE_A,
      usageEvidenceScope: SCOPE_A,
      status: {
        collector: {
          collector: 'error',
          lastError: 'old gateway failure',
        },
      },
      errorLogCount: 4,
      usageError: 'old summary failure',
    });
    expect(evaluateDashboardHealthSummary(switchedScopeInput)).toMatchObject({
      status: 'loading_unconfirmed',
      hasUsageAttention: false,
      hasErrorLogAttention: false,
    });
    expect(hasCollectorAttention(switchedScopeInput)).toBe(false);
  });

  it('T1-4 & P1-1: distinguishes Panel Lite, monitoring disabled, Full Web unconfigured, and service_unavailable even when evidenceScope is empty', () => {
    expect(
      evaluateDashboardHealthSummary(
        makeBaseInput({
          isPanelLite: true,
          usageEnabled: false,
          featureReason: 'service_unavailable',
          evidenceScope: '',
          usageEvidenceScope: '',
          status: null,
          usageLoaded: false,
          errorLogsLoaded: false,
        })
      )
    ).toMatchObject({
      status: 'unavailable_not_applicable',
      unavailableVariant: 'panel_mode_not_applicable',
    });

    expect(
      evaluateDashboardHealthSummary(
        makeBaseInput({
          isPanelLite: false,
          panelHostMode: 'external_panel',
          managerServiceAvailable: false,
          usageEnabled: false,
          featureReason: 'service_not_configured',
          evidenceScope: '',
          usageEvidenceScope: '',
          status: null,
          usageLoaded: false,
          errorLogsLoaded: false,
        })
      )
    ).toMatchObject({
      status: 'unavailable_not_applicable',
      unavailableVariant: 'panel_mode_not_applicable',
    });

    expect(
      evaluateDashboardHealthSummary(
        makeBaseInput({
          isPanelLite: false,
          panelHostMode: 'manager_embedded',
          managerServiceAvailable: true,
          usageEnabled: false,
          featureReason: 'monitoring_disabled',
          evidenceScope: '',
          usageEvidenceScope: '',
          status: null,
          usageLoaded: false,
          errorLogsLoaded: false,
        })
      )
    ).toMatchObject({
      status: 'unavailable_not_applicable',
      unavailableVariant: 'monitoring_disabled',
    });

    expect(
      evaluateDashboardHealthSummary(
        makeBaseInput({
          isPanelLite: false,
          panelHostMode: 'manager_embedded',
          managerServiceAvailable: true,
          usageEnabled: false,
          featureReason: 'service_not_configured',
          evidenceScope: '',
          usageEvidenceScope: '',
          status: null,
          usageLoaded: false,
          errorLogsLoaded: false,
        })
      )
    ).toMatchObject({
      status: 'unavailable_not_applicable',
      unavailableVariant: 'usage_not_configured',
    });

    const unreachableManagerInput = makeBaseInput({
      isPanelLite: false,
      panelHostMode: 'manager_embedded',
      managerServiceAvailable: false,
      usageEnabled: false,
      featureReason: 'service_unavailable',
      evidenceScope: '',
      usageEvidenceScope: '',
      status: null,
      usageLoaded: false,
      errorLogsLoaded: false,
    });
    expect(evaluateDashboardHealthSummary(unreachableManagerInput)).toMatchObject({
      status: 'attention_required',
      collector: {
        state: 'attention_required',
        reasons: ['service_unavailable'],
      },
    });
    expect(hasCollectorAttention(unreachableManagerInput)).toBe(true);
  });

  it('T1-5: resolves top-level vs nested deadLetters conflicts and flags all collector anomaly branches', () => {
    const nestedDeadLetters: UsageServiceStatus = {
      deadLetters: 0,
      collector: { collector: 'running', deadLetters: 3 },
    };
    const topDeadLetters: UsageServiceStatus = {
      deadLetters: 4,
      collector: { collector: 'running', deadLetters: 0 },
    };

    expect(resolveCollectorDeadLetters(nestedDeadLetters)).toBe(3);
    expect(resolveCollectorDeadLetters(topDeadLetters)).toBe(4);

    for (const anomaly of [
      makeBaseInput({ status: nestedDeadLetters }),
      makeBaseInput({ status: topDeadLetters }),
      makeBaseInput({ error: 'status endpoint offline' }),
      makeBaseInput({
        status: { collector: { collector: 'running', lastError: 'connection refused' } },
      }),
      makeBaseInput({ status: { collector: { collector: 'stopped' } } }),
      makeBaseInput({ status: { collector: { collector: 'error' } } }),
      makeBaseInput({
        status: {
          collector: { collector: 'running' },
          database: { checkpoint: { error: 'database is locked' } },
        },
      }),
    ]) {
      expect(evaluateCollectorHealth(anomaly).state).toBe('attention_required');
      expect(hasCollectorAttention(anomaly)).toBe(true);
      expect(evaluateDashboardHealthSummary(anomaly).status).toBe('attention_required');
    }
  });

  it('T1-7: surfaces version updates only for trusted newer candidates and suppresses up-to-date or stale states', () => {
    const freshUpdateStatus: UpdateStatus = {
      current_version: '2.0.0-beta.1',
      source_commit: '4231248cf111887a4afdd5ef82099943754cdddf',
      channel_preference: 'beta',
      channel: 'beta',
      automatic: true,
      state: 'update_available',
      stale: false,
      last_success_at: '2026-11-13T00:00:00Z',
      target: {
        schema_version: 1,
        release: {
          version: '2.0.0-beta.2',
          stage: 'beta',
          source_commit: 'abcdef1234567890abcdef1234567890abcdef12',
        },
        content: { summary: {}, notes: {} },
        update: {
          breaking: false,
          migration_required: false,
          minimum_direct_upgrade_version: null,
          upgrade_guide_url: '',
        },
        distribution: {
          docker: { image: 'ghcr.io/seakee/cpa-manager-plus', version_tag: 'v2.0.0-beta.2' },
          native: { assets: [] },
        },
        compatibility: { minimum_cpa_version: '8.0.55' },
      },
    };

    expect(
      evaluateDashboardVersionUpdates({
        appVersion: '2.0.0-beta.1',
        apiVersion: '8.0.55',
        latestApiVersion: '8.0.55',
        managerServiceAvailable: true,
        managerUpdatesAvailable: true,
        managerUpdatesError: false,
        managerUpdateStatus: freshUpdateStatus,
        externalManagerUpdateFallback: false,
        externalStableVersion: null,
        externalStableError: false,
      })
    ).toMatchObject({
      managerHostedUpdateAvailable: true,
      coreApiUpdateAvailable: false,
      hasAnyUpdate: true,
    });

    expect(
      evaluateDashboardVersionUpdates({
        appVersion: '2.0.0-beta.1',
        apiVersion: '8.0.55',
        latestApiVersion: '8.0.55',
        managerServiceAvailable: true,
        managerUpdatesAvailable: true,
        managerUpdatesError: false,
        managerUpdateStatus: { ...freshUpdateStatus, stale: true },
        externalManagerUpdateFallback: true,
        externalStableVersion: '2.0.0-beta.1',
        externalStableError: false,
      })
    ).toMatchObject({
      managerHostedUpdateAvailable: false,
      externalManagerUpdateAvailable: false,
      coreApiUpdateAvailable: false,
      hasAnyUpdate: false,
    });

    expect(
      evaluateDashboardVersionUpdates({
        appVersion: '1.12.10',
        apiVersion: '8.0.55',
        latestApiVersion: '8.0.56',
        managerServiceAvailable: false,
        managerUpdatesAvailable: false,
        managerUpdatesError: false,
        managerUpdateStatus: null,
        externalManagerUpdateFallback: true,
        externalStableVersion: '1.12.11',
        externalStableError: false,
      })
    ).toMatchObject({
      externalManagerUpdateAvailable: true,
      coreApiUpdateAvailable: true,
      hasAnyUpdate: true,
    });
  });
});
