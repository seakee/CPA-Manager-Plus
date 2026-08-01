import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { TFunction } from 'i18next';
import en from '@/i18n/locales/en.json';
import { probeXaiInference, probeXaiQuota } from '@/utils/quota/providerRequests';
import { XaiProbeError, classifyXaiProbe, parseXaiErrorEnvelope } from '@/utils/quota/xaiErrors';
import type {
  CodexInspectionLogDetail,
  CodexInspectionLogLevel,
} from '@/features/monitoring/codexInspection';
import { DEFAULT_CODEX_INSPECTION_SETTINGS } from './codexInspectionSettings';
import { inspectSingleXaiAccount } from './xaiInspectionProbe';

vi.mock('@/utils/quota/providerRequests', () => ({
  probeXaiInference: vi.fn(),
  probeXaiQuota: vi.fn(),
}));

const mockProbeXaiInference = vi.mocked(probeXaiInference);
const mockProbeXaiQuota = vi.mocked(probeXaiQuota);
const settings = {
  baseUrl: '',
  token: '',
  ...DEFAULT_CODEX_INSPECTION_SETTINGS,
  targetTypes: ['xai'],
  targetType: 'xai',
  xaiInferenceEnabled: true,
  usedPercentThreshold: 100,
};

const inspectionT = ((key: string, values?: Record<string, unknown>) => {
  const template = key.split('.').reduce<unknown>((current, segment) => {
    if (!current || typeof current !== 'object' || Array.isArray(current)) return undefined;
    return (current as Record<string, unknown>)[segment];
  }, en);
  return String(template ?? key).replace(/{{\s*([^}\s]+)\s*}}/g, (_, name: string) =>
    String(values?.[name] ?? `{{${name}}}`)
  );
}) as TFunction;

const captureLogs = () => {
  const logs: Array<{
    level: string;
    message: string;
    detail?: CodexInspectionLogDetail;
  }> = [];
  return {
    logs,
    onLog: (level: CodexInspectionLogLevel, message: string, detail?: CodexInspectionLogDetail) =>
      logs.push({ level, message, detail }),
  };
};
const rawAccount = {
  name: 'xai-auth.json',
  type: 'xai',
  auth_index: 'xai-1',
  account: 'xai-user@example.test',
};
const baseAccount = {
  key: 'xai-auth.json::xai-1',
  fileName: 'xai-auth.json',
  displayAccount: 'xai-user@example.test',
  authIndex: 'xai-1',
  accountId: null,
  provider: 'xai',
  disabled: false,
  autoRecoverOwned: false,
  status: '',
  state: '',
  raw: rawAccount,
};

const healthySummary = {
  periodType: 'weekly' as const,
  usagePercent: 25,
  periodEnd: '2026-07-22T00:00:00Z',
  productUsage: [{ product: 'Grok 4', usagePercent: 30 }],
  monthlyLimitCents: 10000,
  usedCents: 4000,
  includedUsedCents: null,
  onDemandCapCents: null,
  onDemandUsedCents: null,
  onDemandUsedPercent: null,
  billingPeriodEnd: '2026-08-01T00:00:00Z',
  usedPercent: 40,
};

const officialApiSummary = {
  ...healthySummary,
  periodType: 'unknown' as const,
  usagePercent: null,
  productUsage: [],
  monthlyLimitCents: null,
  usedCents: null,
  billingPeriodEnd: undefined,
  usedPercent: null,
  officialApiHealth: {
    source: 'api.x.ai/v1/me' as const,
    userId: 'user-1',
    teamId: 'team-1',
    teamBlocked: false,
  },
};

const inferenceError = (statusCode: number, body: unknown) => {
  const envelope = parseXaiErrorEnvelope({ statusCode, body });
  return new XaiProbeError(
    `HTTP ${statusCode}`,
    envelope,
    classifyXaiProbe({ surface: 'inference', envelope })
  );
};

const billingError = (statusCode: number, body: unknown) => {
  const envelope = parseXaiErrorEnvelope({ statusCode, body });
  return new XaiProbeError(
    `HTTP ${statusCode}`,
    envelope,
    classifyXaiProbe({ surface: 'billing', envelope })
  );
};

describe('inspectSingleXaiAccount', () => {
  beforeEach(() => {
    mockProbeXaiInference.mockReset();
    mockProbeXaiQuota.mockReset();
    mockProbeXaiInference.mockResolvedValue({ statusCode: 200 });
    mockProbeXaiQuota.mockResolvedValue({
      summary: healthySummary,
      failures: [],
      partial: false,
      source: 'billing',
      statusCode: 200,
    });
  });

  it('labels billing-only health as billing evidence rather than real inference', async () => {
    const { logs, onLog } = captureLogs();

    await inspectSingleXaiAccount(
      baseAccount,
      { ...settings, xaiInferenceEnabled: false },
      onLog,
      inspectionT
    );

    expect(logs).toHaveLength(1);
    expect(logs[0]).toMatchObject({ level: 'info' });
    expect(logs[0].message).toContain('billing healthy');
    expect(logs[0].message).not.toContain('real inference healthy');
    expect(logs[0].detail).toMatchObject({
      provider: 'xai',
      fileName: 'xai-auth.json',
      displayAccount: 'xai-user@example.test',
      inspectionMode: 'billing',
      healthEvidence: 'billing_healthy',
      billingAvailable: true,
      inferenceEnabled: false,
      action: 'keep',
      statusCode: 200,
      usedPercent: 40,
    });
  });

  it('labels partial billing as a warning and states that inference was not verified', async () => {
    mockProbeXaiQuota.mockResolvedValue({
      summary: healthySummary,
      failures: [billingError(503, { error: 'monthly billing unavailable' })],
      partial: true,
      source: 'billing',
      statusCode: 200,
    });
    const { logs, onLog } = captureLogs();

    await inspectSingleXaiAccount(
      baseAccount,
      { ...settings, xaiInferenceEnabled: false },
      onLog,
      inspectionT
    );

    expect(logs[0]).toMatchObject({ level: 'warning' });
    expect(logs[0].message).toContain('billing partially available');
    expect(logs[0].message).not.toContain('real inference healthy');
  });

  it('uses real inference evidence only when the inference request completes', async () => {
    const { logs, onLog } = captureLogs();

    await inspectSingleXaiAccount(baseAccount, settings, onLog, inspectionT);

    expect(logs[0]).toMatchObject({ level: 'info' });
    expect(logs[0].message).toContain('real inference healthy');
    expect(logs[0].detail).toMatchObject({
      inspectionMode: 'inference',
      healthEvidence: 'inference_healthy',
      billingAvailable: true,
      inferenceEnabled: true,
      inferenceHealthy: true,
      action: 'keep',
      statusCode: 200,
      usedPercent: 40,
    });
  });

  it('uses inference wording for transport and protocol failures', async () => {
    const transportLogs = captureLogs();
    mockProbeXaiInference.mockRejectedValue(new Error('inference network down'));

    await inspectSingleXaiAccount(baseAccount, settings, transportLogs.onLog, inspectionT);

    expect(transportLogs.logs[0].message).toContain('Real inference check');
    expect(transportLogs.logs[0].message).toContain('inference network down');
    expect(transportLogs.logs[0].message).not.toContain('Billing check');
    expect(transportLogs.logs[0].detail).toMatchObject({
      inspectionMode: 'inference',
      healthEvidence: 'request_error',
      inferenceEnabled: true,
      inferenceHealthy: false,
      action: 'keep',
    });

    const protocolLogs = captureLogs();
    mockProbeXaiInference.mockRejectedValue(inferenceError(200, { type: 'response.in_progress' }));
    await inspectSingleXaiAccount(baseAccount, settings, protocolLogs.onLog, inspectionT);

    expect(protocolLogs.logs[0].message).toContain('inference endpoint');
    expect(protocolLogs.logs[0].message).not.toContain('billing endpoint');
  });

  it('uses billing or identity probes only when real inference is disabled', async () => {
    const result = await inspectSingleXaiAccount(baseAccount, {
      ...settings,
      xaiInferenceEnabled: false,
    });

    expect(mockProbeXaiQuota).toHaveBeenCalledWith(rawAccount, expect.any(Function), {
      timeout: settings.timeout,
    });
    expect(mockProbeXaiInference).not.toHaveBeenCalled();
    expect(result).toMatchObject({
      action: 'keep',
      statusCode: 200,
      usedPercent: 40,
      autoRecoverEligible: false,
      planType: null,
      errorKind: 'billing_healthy',
      actionReason: 'monitoring.xai_inspection_reason_billing_healthy',
    });
  });

  it('normalizes official API identity health to the shared healthy classification', async () => {
    mockProbeXaiQuota.mockResolvedValue({
      summary: officialApiSummary,
      failures: [],
      partial: false,
      source: 'official-api',
      statusCode: 200,
    });

    const { logs, onLog } = captureLogs();
    const result = await inspectSingleXaiAccount(
      baseAccount,
      { ...settings, xaiInferenceEnabled: false },
      onLog,
      inspectionT
    );

    expect(result).toMatchObject({
      action: 'keep',
      errorKind: 'official_api_healthy',
      actionReason: en.monitoring.xai_inspection_reason_official_api_healthy,
    });
    expect(logs[0].detail).toMatchObject({
      inspectionMode: 'identity',
      healthEvidence: 'official_api_healthy',
      billingAvailable: false,
      billingPartial: false,
      inferenceEnabled: false,
    });
  });

  it('does not auto-enable an inspection-owned credential without real inference evidence', async () => {
    const result = await inspectSingleXaiAccount(
      { ...baseAccount, disabled: true, autoRecoverOwned: true },
      { ...settings, xaiInferenceEnabled: false }
    );

    expect(result).toMatchObject({
      action: 'keep',
      autoRecoverEligible: false,
      actionReason: 'monitoring.xai_inspection_reason_billing_healthy',
    });
  });

  it('keeps non-blocking partial billing as a visible non-error result', async () => {
    mockProbeXaiQuota.mockResolvedValue({
      summary: healthySummary,
      failures: [billingError(503, { error: 'monthly billing unavailable' })],
      partial: true,
      source: 'billing',
      statusCode: 200,
    });

    const result = await inspectSingleXaiAccount(baseAccount, {
      ...settings,
      xaiInferenceEnabled: false,
    });

    expect(result).toMatchObject({
      action: 'keep',
      errorKind: 'billing_partial',
      actionReason: 'monitoring.xai_inspection_reason_billing_partial',
      usedPercent: 40,
    });
  });

  it('prioritizes a blocking partial billing failure while retaining quota windows', async () => {
    const blockingFailure = billingError(402, {
      code: 'personal-team-blocked:spending-limit',
    });
    mockProbeXaiQuota.mockResolvedValue({
      summary: healthySummary,
      failures: [blockingFailure],
      partial: true,
      source: 'billing',
      blockingFailure,
    });

    const result = await inspectSingleXaiAccount(baseAccount, {
      ...settings,
      xaiInferenceEnabled: false,
    });

    expect(result).toMatchObject({
      action: 'disable',
      statusCode: 402,
      errorKind: 'spending_limit',
      usedPercent: 40,
    });
    expect(result.quotaWindows).not.toHaveLength(0);
  });

  it('does not retry a permanent billing failure', async () => {
    mockProbeXaiQuota.mockRejectedValue(
      billingError(402, { code: 'personal-team-blocked:spending-limit' })
    );

    await inspectSingleXaiAccount(baseAccount, {
      ...settings,
      retries: 2,
      xaiInferenceEnabled: false,
    });

    expect(mockProbeXaiQuota).toHaveBeenCalledTimes(1);
  });

  it('retries a transient billing failure', async () => {
    mockProbeXaiQuota
      .mockRejectedValueOnce(billingError(429, { error: 'too many requests' }))
      .mockResolvedValueOnce({
        summary: healthySummary,
        failures: [],
        partial: false,
        source: 'billing',
      });

    const result = await inspectSingleXaiAccount(baseAccount, {
      ...settings,
      retries: 2,
      xaiInferenceEnabled: false,
    });

    expect(mockProbeXaiQuota).toHaveBeenCalledTimes(2);
    expect(result.errorKind).toBe('billing_healthy');
  });

  it('uses a real inference request as the health authority and keeps billing quota display', async () => {
    const result = await inspectSingleXaiAccount(baseAccount, settings);

    expect(mockProbeXaiQuota).toHaveBeenCalledWith(rawAccount, expect.any(Function), {
      timeout: settings.timeout,
    });
    expect(mockProbeXaiInference).toHaveBeenCalledWith(
      rawAccount,
      expect.any(Function),
      { timeout: settings.timeout },
      {
        model: settings.xaiInferenceModel,
        prompt: settings.xaiInferencePrompt,
        userAgent: settings.xaiInferenceUserAgent,
      }
    );
    expect(result).toMatchObject({
      action: 'keep',
      statusCode: 200,
      usedPercent: 40,
      planType: null,
      errorKind: 'inference_healthy',
      actionReason: 'monitoring.xai_inspection_reason_inference_healthy',
    });
    expect((result.quotaWindows ?? []).map((window) => window.id)).toEqual([
      'xai-weekly',
      'xai-monthly',
      'xai-product-0',
    ]);
  });

  it('routes real inference through the official API after verified identity fallback', async () => {
    mockProbeXaiQuota.mockResolvedValue({
      summary: officialApiSummary,
      failures: [],
      partial: false,
      source: 'official-api',
      statusCode: 200,
    });

    const result = await inspectSingleXaiAccount(baseAccount, settings);

    expect(mockProbeXaiInference).toHaveBeenCalledWith(
      rawAccount,
      expect.any(Function),
      { timeout: settings.timeout },
      {
        model: settings.xaiInferenceModel,
        prompt: settings.xaiInferencePrompt,
        userAgent: settings.xaiInferenceUserAgent,
        routeMode: 'official',
      }
    );
    expect(result).toMatchObject({
      action: 'keep',
      statusCode: 200,
      usedPercent: null,
      errorKind: 'inference_healthy',
    });
    expect(result.quotaWindows).toEqual([]);
  });

  it('does not render a zero-cap on-demand window without usage evidence', async () => {
    mockProbeXaiQuota.mockResolvedValue({
      summary: {
        ...healthySummary,
        onDemandCapCents: 0,
        onDemandUsedCents: 0,
        onDemandUsedPercent: null,
      },
      failures: [],
      partial: false,
      source: 'billing',
    });

    const result = await inspectSingleXaiAccount(baseAccount, settings);

    expect((result.quotaWindows ?? []).map((window) => window.id)).not.toContain('xai-on-demand');
  });

  it('does not treat unavailable billing as an unhealthy credential when inference succeeds', async () => {
    mockProbeXaiQuota.mockRejectedValue(new Error('billing endpoint unavailable'));
    const { logs, onLog } = captureLogs();

    const result = await inspectSingleXaiAccount(baseAccount, settings, onLog, inspectionT);

    expect(mockProbeXaiInference).toHaveBeenCalledWith(
      rawAccount,
      expect.any(Function),
      { timeout: settings.timeout },
      {
        model: settings.xaiInferenceModel,
        prompt: settings.xaiInferencePrompt,
        userAgent: settings.xaiInferenceUserAgent,
      }
    );
    expect(result).toMatchObject({
      action: 'keep',
      statusCode: 200,
      usedPercent: null,
      errorKind: 'inference_healthy',
    });
    expect(logs[0].detail).toMatchObject({
      inspectionMode: 'inference',
      healthEvidence: 'inference_healthy',
      billingAvailable: false,
      billingPartial: true,
      inferenceHealthy: true,
    });
  });

  it('only auto-enables an inspection-owned disabled credential after real inference succeeds', async () => {
    const manual = await inspectSingleXaiAccount(
      { ...baseAccount, disabled: true, autoRecoverOwned: false },
      settings
    );
    const owned = await inspectSingleXaiAccount(
      { ...baseAccount, disabled: true, autoRecoverOwned: true },
      settings
    );

    expect(manual).toMatchObject({
      action: 'keep',
      actionReason: 'monitoring.xai_inspection_reason_inference_manual_disable',
      autoRecoverEligible: false,
    });
    expect(owned).toMatchObject({ action: 'enable', autoRecoverEligible: true });
  });

  it.each([
    {
      name: 'expired credentials',
      statusCode: 401,
      body: { code: 'unauthenticated:bad-credentials' },
      action: 'reauth',
      errorKind: 'auth_invalid',
      actionReason: 'monitoring.xai_inspection_reason_auth_invalid',
      isQuota: false,
    },
    {
      name: 'ambiguous quota response',
      statusCode: 402,
      body: { error: 'Payment required' },
      action: 'keep',
      errorKind: 'quota_or_entitlement_unknown',
      actionReason: 'monitoring.xai_inspection_reason_inference_quota_unknown',
      isQuota: false,
    },
    {
      name: 'rate limiting',
      statusCode: 429,
      body: { error: 'Too many requests' },
      action: 'keep',
      errorKind: 'rate_limited',
      actionReason: 'monitoring.xai_inspection_reason_rate_limited',
      isQuota: false,
    },
    {
      name: 'unavailable model',
      statusCode: 404,
      body: { error: 'Model not found' },
      action: 'keep',
      errorKind: 'model_unavailable',
      actionReason: 'monitoring.xai_inspection_reason_model_unavailable',
      isQuota: false,
    },
    {
      name: 'missing entitlement',
      statusCode: 403,
      body: { error: 'Need a Grok subscription' },
      action: 'disable',
      errorKind: 'entitlement_denied',
      actionReason: 'monitoring.xai_inspection_reason_entitlement_disable',
      isQuota: true,
    },
  ])(
    'uses inference status for $name',
    async ({ action, actionReason, body, errorKind, isQuota, statusCode }) => {
      mockProbeXaiInference.mockRejectedValue(inferenceError(statusCode, body));

      const result = await inspectSingleXaiAccount(baseAccount, settings);

      expect(result).toMatchObject({ action, actionReason, errorKind, isQuota, statusCode });
    }
  );

  it('keeps an already-disabled xAI credential disabled when inference finds a blocking issue', async () => {
    mockProbeXaiInference.mockRejectedValue(
      inferenceError(402, { code: 'personal-team-blocked:spending-limit' })
    );

    const result = await inspectSingleXaiAccount({ ...baseAccount, disabled: true }, settings);

    expect(result).toMatchObject({
      action: 'keep',
      actionReason: 'monitoring.xai_inspection_reason_spending_limit_disabled',
      errorKind: 'spending_limit',
      isQuota: true,
    });
  });

  it('logs destructive delete suggestions with error severity', async () => {
    const envelope = parseXaiErrorEnvelope({
      statusCode: 410,
      body: { code: 'credential_removed', error: 'credential no longer exists' },
    });
    mockProbeXaiInference.mockRejectedValue(
      new XaiProbeError('credential removed', envelope, {
        classification: 'unknown',
        suggestedAction: 'delete',
        reasonCode: 'xai_credential_removed',
        confidence: 'verified',
        needsReview: false,
        retryAfterSeconds: null,
      })
    );
    const { logs, onLog } = captureLogs();

    const result = await inspectSingleXaiAccount(baseAccount, settings, onLog, inspectionT);

    expect(result.action).toBe('delete');
    expect(logs[0]).toMatchObject({ level: 'error' });
  });

  it('keeps the credential unchanged when inference completes without a completion event', async () => {
    mockProbeXaiInference.mockRejectedValue(inferenceError(200, { type: 'response.in_progress' }));

    const result = await inspectSingleXaiAccount(baseAccount, settings);

    expect(result).toMatchObject({
      action: 'keep',
      actionReason: 'monitoring.xai_inspection_reason_inference_protocol_changed',
      statusCode: 200,
      errorKind: 'protocol_changed',
    });
  });
});
