import { beforeEach, describe, expect, it, vi } from 'vitest';
import { requestCodexUsageRaw } from '@/services/api/codexQuota';
import { DEFAULT_CODEX_INSPECTION_SETTINGS } from './codexInspectionSettings';
import { inspectSingleAccount, toInspectionAccount } from './codexInspectionProbe';

vi.mock('@/services/api/codexQuota', () => ({
  requestCodexUsageRaw: vi.fn(),
}));

const mockRequestCodexUsageRaw = vi.mocked(requestCodexUsageRaw);

const baseAccount = toInspectionAccount({
  name: 'codex-auth.json',
  type: 'codex',
  auth_index: 'auth-1',
  account: 'user@example.test',
});

const settings = {
  baseUrl: '',
  token: '',
  ...DEFAULT_CODEX_INSPECTION_SETTINGS,
  usedPercentThreshold: 100,
};

const createDisabledAccount = (autoRecoverOwned: boolean) => ({
  ...baseAccount,
  disabled: true,
  autoRecoverOwned,
});

const createUsageResult = (usedPercent: number, extraWindows = {}) => ({
  result: {
    statusCode: 200,
    hasStatusCode: true,
    header: {},
    bodyText: '',
    body: {},
  },
  payload: {
    user_id: 'user-test',
    account_id: 'acct-test',
    email: 'user@example.test',
    plan_type: 'free',
    rate_limit: {
      allowed: true,
      limit_reached: false,
      primary_window: {
        used_percent: usedPercent,
        limit_window_seconds: 2_592_000,
        reset_after_seconds: 2_592_000,
        reset_at: 1_782_895_966,
      },
      secondary_window: null,
      ...extraWindows,
    },
    code_review_rate_limit: null,
    additional_rate_limits: null,
  },
});

describe('inspectSingleAccount', () => {
  beforeEach(() => {
    mockRequestCodexUsageRaw.mockReset();
  });

  it('keeps an enabled account when the monthly Codex quota is still available', async () => {
    mockRequestCodexUsageRaw.mockResolvedValue(createUsageResult(5));

    const result = await inspectSingleAccount(baseAccount, settings);

    expect(result.action).toBe('keep');
    expect(result.actionReason).toBe('月额度仍可用，无需处理');
    expect(result.usedPercent).toBe(5);
    expect(result.isQuota).toBe(false);
    expect(result.planType).toBe('free');
    expect(result.quotaWindows).toEqual([
      expect.objectContaining({
        id: 'monthly',
        labelKey: 'codex_quota.monthly_window',
        usedPercent: 5,
        limitWindowSeconds: 2_592_000,
      }),
    ]);
  });

  it('disables an enabled account when the monthly Codex quota reaches the threshold', async () => {
    mockRequestCodexUsageRaw.mockResolvedValue(createUsageResult(100));

    const result = await inspectSingleAccount(baseAccount, settings);

    expect(result.action).toBe('disable');
    expect(result.actionReason).toBe('月额度达到阈值，建议禁用账号');
    expect(result.usedPercent).toBe(100);
    expect(result.isQuota).toBe(true);
  });

  it('keeps an enabled account when only the short window is exhausted', async () => {
    mockRequestCodexUsageRaw.mockResolvedValue(
      createUsageResult(5, {
        primary_window: {
          used_percent: 100,
          limit_window_seconds: 18_000,
        },
        secondary_window: {
          used_percent: 5,
          limit_window_seconds: 2_592_000,
        },
      })
    );

    const result = await inspectSingleAccount(baseAccount, settings);

    expect(result.action).toBe('keep');
    expect(result.actionReason).toBe('5 小时额度达到阈值，但月额度仍可用，暂不禁用账号');
    expect(result.usedPercent).toBe(5);
    expect(result.isQuota).toBe(false);
  });

  it('treats team secondary windows without duration as monthly quota', async () => {
    mockRequestCodexUsageRaw.mockResolvedValue({
      result: {
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {},
      },
      payload: {
        plan_type: 'team',
        rate_limit: {
          primary_window: {
            used_percent: 100,
          },
          secondary_window: {
            used_percent: 5,
          },
        },
      },
    });

    const result = await inspectSingleAccount(baseAccount, settings);

    expect(result.action).toBe('keep');
    expect(result.actionReason).toBe('5 小时额度达到阈值，但月额度仍可用，暂不禁用账号');
    expect(result.usedPercent).toBe(5);
    expect((result.quotaWindows ?? []).map((window) => window.id)).toEqual([
      'five-hour',
      'monthly',
    ]);
  });

  it('deletes an account when the workspace is deactivated', async () => {
    mockRequestCodexUsageRaw.mockResolvedValue({
      result: {
        statusCode: 402,
        hasStatusCode: true,
        header: {},
        bodyText: '{"detail":{"code":"deactivated_workspace"}}',
        body: { detail: { code: 'deactivated_workspace' } },
      },
      payload: null,
    });

    const result = await inspectSingleAccount(baseAccount, settings);

    expect(result.action).toBe('delete');
    expect(result.actionReason).toBe('接口返回 402，工作区已停用，建议删除账号');
    expect(result.usedPercent).toBe(null);
    expect(result.isQuota).toBe(false);
    expect(result.errorKind).toBe('http_status');
    expect(result.errorDetail).toContain('deactivated_workspace');
  });

  it('reauthenticates an account when the Codex token is invalidated', async () => {
    mockRequestCodexUsageRaw.mockResolvedValue({
      result: {
        statusCode: 401,
        hasStatusCode: true,
        header: {},
        bodyText: '{"message":"Your authentication token has been invalidated."}',
        body: { message: 'Your authentication token has been invalidated.' },
      },
      payload: null,
    });

    const result = await inspectSingleAccount(baseAccount, settings);

    expect(result.action).toBe('reauth');
    expect(result.actionReason).toBe('接口返回 401，认证令牌已失效，建议重新登录账号');
    expect(result.errorKind).toBe('http_status');
  });

  it('reauthenticates an account for unknown 401 authentication failures', async () => {
    mockRequestCodexUsageRaw.mockResolvedValue({
      result: {
        statusCode: 401,
        hasStatusCode: true,
        header: {},
        bodyText: '{"message":"unauthorized"}',
        body: { message: 'unauthorized' },
      },
      payload: null,
    });

    const result = await inspectSingleAccount(baseAccount, settings);

    expect(result.action).toBe('reauth');
    expect(result.actionReason).toBe('接口返回 401，认证失败，建议重新登录账号');
    expect(result.errorKind).toBe('http_status');
  });

  it('keeps regular 402 quota responses as disable suggestions', async () => {
    mockRequestCodexUsageRaw.mockResolvedValue({
      result: {
        statusCode: 402,
        hasStatusCode: true,
        header: {},
        bodyText: '{"message":"limit reached"}',
        body: { message: 'limit reached' },
      },
      payload: null,
    });

    const result = await inspectSingleAccount(baseAccount, settings);

    expect(result.action).toBe('disable');
    expect(result.actionReason).toBe('额度已耗尽，建议禁用账号');
    expect(result.isQuota).toBe(true);
    expect(result.errorKind).toBe('http_status');
    expect(result.errorDetail).toContain('limit reached');
  });

  it('keeps accounts with missing status code and preserves response detail', async () => {
    mockRequestCodexUsageRaw.mockResolvedValue({
      result: {
        statusCode: 0,
        hasStatusCode: false,
        header: {},
        bodyText: '{"error":"proxy response missing status"}',
        body: { error: 'proxy response missing status' },
      },
      payload: {
        plan_type: 'team',
        rate_limit: {
          primary_window: {
            used_percent: 12,
            limit_window_seconds: 18_000,
          },
          secondary_window: {
            used_percent: 34,
            limit_window_seconds: 2_592_000,
          },
        },
      },
    });

    const result = await inspectSingleAccount(baseAccount, settings);

    expect(result.action).toBe('keep');
    expect(result.errorKind).toBe('missing_status');
    expect(result.errorDetail).toContain('proxy response missing status');
    expect(result.planType).toBe('team');
    expect((result.quotaWindows ?? []).map((window) => window.id)).toEqual([
      'five-hour',
      'monthly',
    ]);
  });

  it('keeps accounts when the probe request fails and preserves error detail', async () => {
    mockRequestCodexUsageRaw.mockRejectedValue(new Error('network failed'));

    const result = await inspectSingleAccount(baseAccount, settings);

    expect(result.action).toBe('keep');
    expect(result.errorKind).toBe('request_error');
    expect(result.errorDetail).toBe('network failed');
  });

  it('keeps a disabled account when a successful response has no quota data', async () => {
    mockRequestCodexUsageRaw.mockResolvedValue({
      result: {
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '{"ok":true}',
        body: { ok: true },
      },
      payload: null,
    });

    const result = await inspectSingleAccount(createDisabledAccount(true), settings);

    expect(result).toMatchObject({
      action: 'keep',
      usedPercent: null,
      isQuota: false,
      autoRecoverEligible: false,
    });
  });

  it('keeps a disabled account while the five-hour window is exhausted', async () => {
    mockRequestCodexUsageRaw.mockResolvedValue(
      createUsageResult(5, {
        primary_window: {
          used_percent: 100,
          limit_window_seconds: 18_000,
        },
        secondary_window: {
          used_percent: 5,
          limit_window_seconds: 2_592_000,
        },
      })
    );

    const result = await inspectSingleAccount(createDisabledAccount(true), settings);

    expect(result).toMatchObject({
      action: 'keep',
      actionReason: '5 小时额度仍达到阈值，月额度可用但继续保持禁用',
      usedPercent: 5,
      isQuota: true,
      autoRecoverEligible: false,
    });
  });

  it('leaves a healthy manually disabled account eligible for manual recovery only', async () => {
    mockRequestCodexUsageRaw.mockResolvedValue(createUsageResult(5));

    const result = await inspectSingleAccount(createDisabledAccount(false), settings);

    expect(result.action).toBe('enable');
    expect(result.autoRecoverEligible).toBe(false);
    expect(result.actionReason).toContain('仅允许手动启用');
  });

  it('marks a healthy inspection-owned disable as auto-recoverable', async () => {
    mockRequestCodexUsageRaw.mockResolvedValue(createUsageResult(5));

    const result = await inspectSingleAccount(createDisabledAccount(true), settings);

    expect(result).toMatchObject({
      action: 'enable',
      usedPercent: 5,
      isQuota: false,
      autoRecoverEligible: true,
    });
  });
});
