import { beforeEach, describe, expect, it, vi } from 'vitest';
import { probeXaiInference, probeXaiQuota } from '@/utils/quota/providerRequests';
import { XaiProbeError, classifyXaiProbe, parseXaiErrorEnvelope } from '@/utils/quota/xaiErrors';
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

    const result = await inspectSingleXaiAccount(baseAccount, {
      ...settings,
      xaiInferenceEnabled: false,
    });

    expect(result).toMatchObject({
      action: 'keep',
      errorKind: 'official_api_healthy',
      actionReason: 'monitoring.xai_inspection_reason_official_api_healthy',
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

    const result = await inspectSingleXaiAccount(baseAccount, settings);

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
    },
    {
      name: 'ambiguous quota response',
      statusCode: 402,
      body: { error: 'Payment required' },
      action: 'keep',
      errorKind: 'quota_or_entitlement_unknown',
    },
    {
      name: 'rate limiting',
      statusCode: 429,
      body: { error: 'Too many requests' },
      action: 'keep',
      errorKind: 'rate_limited',
    },
  ])('uses inference status for $name', async ({ action, body, errorKind, statusCode }) => {
    mockProbeXaiInference.mockRejectedValue(inferenceError(statusCode, body));

    const result = await inspectSingleXaiAccount(baseAccount, settings);

    expect(result).toMatchObject({ action, errorKind, statusCode });
  });

  it('keeps the credential unchanged when inference completes without a completion event', async () => {
    mockProbeXaiInference.mockRejectedValue(inferenceError(200, { type: 'response.in_progress' }));

    const result = await inspectSingleXaiAccount(baseAccount, settings);

    expect(result).toMatchObject({
      action: 'keep',
      statusCode: 200,
      errorKind: 'protocol_changed',
    });
  });
});
