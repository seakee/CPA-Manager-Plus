import { beforeEach, describe, expect, it, vi } from 'vitest';
import { probeXaiBilling } from '@/utils/quota/providerRequests';
import { XaiProbeError, classifyXaiProbe, parseXaiErrorEnvelope } from '@/utils/quota/xaiErrors';
import { DEFAULT_CODEX_INSPECTION_SETTINGS } from './codexInspectionSettings';
import { inspectSingleXaiAccount } from './xaiInspectionProbe';

vi.mock('@/utils/quota/providerRequests', () => ({
  probeXaiBilling: vi.fn(),
}));

const mockProbeXaiBilling = vi.mocked(probeXaiBilling);
const settings = {
  baseUrl: '',
  token: '',
  ...DEFAULT_CODEX_INSPECTION_SETTINGS,
  targetType: 'xai',
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

const structuredError = (statusCode: number, body: unknown) => {
  const envelope = parseXaiErrorEnvelope({ statusCode, body });
  return new XaiProbeError(
    `HTTP ${statusCode}`,
    envelope,
    classifyXaiProbe({ surface: 'billing', envelope })
  );
};

describe('inspectSingleXaiAccount', () => {
  beforeEach(() => {
    mockProbeXaiBilling.mockReset();
  });

  it('uses billing-only evidence and keeps a healthy enabled account', async () => {
    mockProbeXaiBilling.mockResolvedValue({
      summary: healthySummary,
      failures: [],
      partial: false,
    });

    const result = await inspectSingleXaiAccount(baseAccount, settings);

    expect(mockProbeXaiBilling).toHaveBeenCalledWith(
      rawAccount,
      expect.any(Function),
      { timeout: settings.timeout }
    );

    expect(result).toMatchObject({
      action: 'keep',
      statusCode: 200,
      usedPercent: 40,
      isQuota: false,
      errorKind: 'billing_healthy',
    });
    expect((result.quotaWindows ?? []).map((window) => window.id)).toEqual([
      'xai-weekly',
      'xai-monthly',
      'xai-product-0',
    ]);
  });

  it('keeps an enabled account when billing usage reaches the display threshold without a failure signal', async () => {
    mockProbeXaiBilling.mockResolvedValue({
      summary: { ...healthySummary, usagePercent: 100 },
      failures: [],
      partial: false,
    });

    const result = await inspectSingleXaiAccount(baseAccount, settings);

    expect(result).toMatchObject({ action: 'keep', usedPercent: 100, isQuota: false });
  });

  it('only auto-enables healthy disables owned by inspection', async () => {
    mockProbeXaiBilling.mockResolvedValue({
      summary: healthySummary,
      failures: [],
      partial: false,
    });

    const manual = await inspectSingleXaiAccount(
      { ...baseAccount, disabled: true, autoRecoverOwned: false },
      settings
    );
    const owned = await inspectSingleXaiAccount(
      { ...baseAccount, disabled: true, autoRecoverOwned: true },
      settings
    );

    expect(manual).toMatchObject({ action: 'keep', autoRecoverEligible: false });
    expect(owned).toMatchObject({ action: 'enable', autoRecoverEligible: true });
  });

  it('disables verified free quota exhaustion under HTTP 402', async () => {
    mockProbeXaiBilling.mockRejectedValue(
      structuredError(402, { code: 'subscription:free-usage-exhausted' })
    );

    const result = await inspectSingleXaiAccount(baseAccount, settings);

    expect(result).toMatchObject({
      action: 'disable',
      statusCode: 402,
      isQuota: true,
      errorKind: 'free_quota_exhausted',
    });
  });

  it('keeps generic 403 and 429 responses non-destructive', async () => {
    mockProbeXaiBilling.mockRejectedValueOnce(structuredError(403, { error: 'Forbidden' }));
    const forbidden = await inspectSingleXaiAccount(baseAccount, settings);

    mockProbeXaiBilling.mockRejectedValueOnce(
      structuredError(429, { code: 'rate_limit', error: 'Too many requests' })
    );
    const rateLimited = await inspectSingleXaiAccount(baseAccount, settings);

    expect(forbidden).toMatchObject({ action: 'keep', errorKind: 'permission_unknown' });
    expect(rateLimited).toMatchObject({ action: 'keep', errorKind: 'rate_limited' });
  });

  it('returns reauth instead of delete for invalid OAuth credentials', async () => {
    mockProbeXaiBilling.mockRejectedValue(
      structuredError(401, { code: 'unauthenticated:bad-credentials' })
    );

    const result = await inspectSingleXaiAccount(baseAccount, settings);

    expect(result).toMatchObject({ action: 'reauth', errorKind: 'auth_invalid' });
  });

  it('keeps usable partial billing data while marking the result partial', async () => {
    mockProbeXaiBilling.mockResolvedValue({
      summary: healthySummary,
      failures: [new Error('weekly endpoint unavailable')],
      partial: true,
    });

    const result = await inspectSingleXaiAccount(baseAccount, settings);

    expect(result).toMatchObject({
      action: 'keep',
      usedPercent: 40,
      errorKind: 'billing_partial',
    });
    expect(result.errorDetail).toContain('weekly endpoint unavailable');
  });

  it('does not hide a permission signal behind partial billing success', async () => {
    const forbidden = structuredError(403, { error: 'Forbidden' });
    mockProbeXaiBilling.mockResolvedValue({
      summary: healthySummary,
      failures: [forbidden],
      partial: true,
    });

    const result = await inspectSingleXaiAccount(baseAccount, settings);

    expect(result).toMatchObject({ action: 'keep', errorKind: 'permission_unknown' });
  });
});
