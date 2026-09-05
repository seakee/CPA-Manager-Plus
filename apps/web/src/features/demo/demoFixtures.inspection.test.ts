import { afterEach, describe, expect, it } from 'vitest';
import i18n from '@/i18n';
import {
  buildDemoCodexInspectionSourceFileStatusActionPlans,
  getDemoCodexInspectionActionIdentityKey,
  isDemoCodexInspectionStatusMutationAmbiguous,
  resetDemoCodexInspectionRunState,
  usageServiceApi,
} from '@/services/api/usageService';
import { apiClient } from '@/services/api/client';
import { normalizeConfigResponse } from '@/services/api/transformers';
import { inspectCodexAccounts } from '@/features/monitoring/codexInspection';
import { toServerInspectionLogViewEntry } from '@/features/monitoring/model/codexInspectionPresentation';
import {
  getDemoApiCallResult,
  getDemoAuthFiles,
  getDemoCodexInspectionLocalLogs,
  getDemoCodexInspectionLocalRun,
  getDemoCodexInspectionRun,
  getDemoManagerConfig,
  getDemoRawConfig,
} from './demoFixtures';
import { setDemoMode } from './demoMode';

describe('credential health inspection demo fixtures', () => {
  afterEach(() => {
    resetDemoCodexInspectionRunState();
    setDemoMode(false);
  });

  it('enables Codex and xAI real inference in both inspection modes', () => {
    const managerConfig = getDemoManagerConfig().config.codexInspection;
    const localRun = getDemoCodexInspectionLocalRun();

    expect(managerConfig).toMatchObject({
      targetType: 'codex',
      targetTypes: ['codex', 'xai'],
      xaiInferenceEnabled: true,
      xaiInferenceModel: 'grok-4.5',
      sampleSize: 0,
    });
    expect(localRun.settings).toMatchObject({
      targetTypes: ['codex', 'xai'],
      xaiInferenceEnabled: true,
      xaiInferenceModel: 'grok-4.5',
      sampleSize: 0,
    });
  });

  it('keeps run summary counts aligned with all result scenarios', () => {
    const detail = getDemoCodexInspectionRun();
    const actions = detail.results.map((item) => item.action);
    const providers = new Set(detail.results.map((item) => item.provider));
    const authFiles = getDemoAuthFiles();
    const expectedResultCount = authFiles.files.filter((file) =>
      ['codex', 'xai'].includes(String(file.provider ?? file.type ?? '').toLowerCase())
    ).length;

    expect(authFiles.total).toBe(authFiles.files.length);
    expect(detail.results).toHaveLength(expectedResultCount);
    expect(providers).toEqual(new Set(['codex', 'xai']));
    expect(detail.run.sampledCount).toBe(detail.results.length);
    expect(detail.run.disableCount).toBe(actions.filter((action) => action === 'disable').length);
    expect(detail.run.enableCount).toBe(actions.filter((action) => action === 'enable').length);
    expect(detail.run.reauthCount).toBe(actions.filter((action) => action === 'reauth').length);
    expect(detail.run.keepCount).toBe(actions.filter((action) => action === 'keep').length);
  });

  it('covers healthy, spending-limit, and expired xAI inference results', () => {
    const detail = getDemoCodexInspectionRun();
    const healthy = detail.results.find((item) => item.authIndex === 'xai-ops-01');
    const limited = detail.results.find((item) => item.authIndex === 'xai-email-user-01');
    const expired = detail.results.find((item) => item.authIndex === 'xai-expired-01');

    expect(healthy).toMatchObject({
      statusCode: 200,
      action: 'keep',
      errorKind: 'inference_healthy',
      disabled: true,
      usedPercent: 3,
      planType: null,
    });
    expect(healthy?.quotaWindows?.map((window) => window.id)).toEqual([
      'xai-weekly',
      'xai-product-0',
    ]);
    expect(limited).toMatchObject({
      statusCode: 402,
      action: 'disable',
      errorKind: 'spending_limit',
      isQuota: true,
    });
    expect(expired).toMatchObject({
      statusCode: 401,
      action: 'reauth',
      errorKind: 'auth_invalid',
      usedPercent: 12,
    });
    expect(expired?.quotaWindows?.map((window) => window.id)).toEqual([
      'xai-weekly',
      'xai-product-0',
    ]);
  });

  it('derives production-shaped server logs from the complete inspection run', () => {
    const baseNow = Date.UTC(2026, 6, 23, 12, 0, 0);
    const detail = getDemoCodexInspectionRun(baseNow);
    const accountLogs = detail.logs.filter(
      (item) =>
        item.message === '账号探测完成' ||
        item.message === 'monitoring.xai_inspection_log_server_complete'
    );

    expect(detail.run.settings?.autoActionMode).toBe('none');
    expect(detail.run.settings?.autoRecoverEnabled).toBe(false);
    expect(detail.logs[0]).toMatchObject({
      level: 'info',
      message: '凭证健康巡检开始',
      detail: {
        triggerType: 'scheduled',
        triggerKey: `interval:45:${Math.floor((baseNow - 42 * 60 * 1000) / (45 * 60 * 1000))}`,
      },
    });
    expect(detail.logs[1]).toMatchObject({
      level: 'info',
      message: '凭证健康巡检集合已准备',
      detail: { targetTypes: detail.run.settings?.targetTypes },
    });
    expect(accountLogs).toHaveLength(detail.results.length);
    detail.results.forEach((result) => {
      const log = accountLogs.find((item) => {
        const logDetail = item.detail as Record<string, unknown> | undefined;
        return logDetail?.fileName === result.fileName;
      });
      expect(log).toBeDefined();
      expect(log?.detail).toMatchObject({
        fileName: result.fileName,
        displayAccount: result.displayAccount,
        action: result.action,
        statusCode: result.statusCode,
      });
    });
    expect(detail.logs[detail.logs.length - 1]).toMatchObject({
      level: 'success',
      message: '凭证健康巡检完成',
      detail: {
        deleteCount: detail.run.deleteCount,
        disableCount: detail.run.disableCount,
        enableCount: detail.run.enableCount,
        reauthCount: detail.run.reauthCount,
        keepCount: detail.run.keepCount,
        actionSuccessCount: 0,
        actionFailedCount: 0,
        actionSkippedCount: 0,
        actionNeedsReviewCount: 0,
        actionErrors: [],
      },
    });
  });

  it('uses production-shaped xAI server logs with explicit mode, evidence, and severity', () => {
    const detail = getDemoCodexInspectionRun();
    const logs = detail.logs.filter(
      (item) => item.message === 'monitoring.xai_inspection_log_server_complete'
    );
    const xaiResultCount = detail.results.filter((item) => item.provider === 'xai').length;
    const findByAccount = (displayAccount: string) =>
      logs.find(
        (item) =>
          (item.detail as Record<string, unknown> | undefined)?.displayAccount === displayAccount
      );

    expect(logs).toHaveLength(xaiResultCount);
    expect(findByAccount('oc0demo01@yijihwjw.com')?.detail).toMatchObject({
      provider: 'xai',
      inspectionMode: 'inference',
      healthEvidence: 'inference_healthy',
      billingAvailable: true,
      inferenceEnabled: true,
      inferenceHealthy: true,
      action: 'keep',
    });
    expect(findByAccount('oc1demo02@yijihwjw.com')?.detail).toMatchObject({
      healthEvidence: 'spending_limit',
      inferenceHealthy: false,
      action: 'disable',
    });
    expect(findByAccount('expired.demo@example.com')?.detail).toMatchObject({
      healthEvidence: 'auth_invalid',
      inferenceHealthy: false,
      action: 'reauth',
    });
  });

  it('uses error severity for Codex re-login results in server and local logs', () => {
    const detail = getDemoCodexInspectionRun();
    const codexReauth = detail.results.find(
      (item) => item.provider === 'codex' && item.action === 'reauth'
    );
    const serverLog = detail.logs.find((item) => {
      const logDetail = item.detail as Record<string, unknown> | undefined;
      return logDetail?.fileName === codexReauth?.fileName;
    });
    const localLog = getDemoCodexInspectionLocalLogs().find((item) =>
      Object.values(item.detail ?? {}).includes(codexReauth?.fileName)
    );

    expect(codexReauth).toBeDefined();
    expect(serverLog?.level).toBe('error');
    expect(localLog?.level).toBe('error');
  });

  it('uses localizable conclusion reasons in server and local inspection fixtures', () => {
    const serverReasons = getDemoCodexInspectionRun().results.map((item) => item.actionReason);
    const localReasons = getDemoCodexInspectionLocalRun().results.map((item) => item.actionReason);

    expect(serverReasons.every((reason) => reason.startsWith('monitoring.'))).toBe(true);
    expect(localReasons.every((reason) => reason.startsWith('monitoring.'))).toBe(true);
  });

  it('builds localized local logs for every demo inspection result', () => {
    const baseNow = Date.UTC(2026, 6, 23, 12, 0, 0);
    const t = i18n.getFixedT('zh-CN');
    const detail = getDemoCodexInspectionRun();
    const logs = getDemoCodexInspectionLocalLogs(baseNow, t);

    expect(logs).toHaveLength(detail.results.length + 3);
    expect(logs[0].message).toContain('正在加载认证文件');
    expect(logs[1].message).toContain(`巡检集合 ${detail.run.probeSetCount} 个账号`);
    detail.results.forEach((result) => {
      expect(logs.some((log) => log.message.includes(result.displayAccount))).toBe(true);
    });

    const healthyXai = logs.find((log) => log.message.includes('oc0demo01@yijihwjw.com'));
    const limitedXai = logs.find((log) => log.message.includes('oc1demo02@yijihwjw.com'));
    const expiredXai = logs.find((log) => log.message.includes('expired.demo@example.com'));
    expect(healthyXai).toMatchObject({ level: 'info' });
    expect(healthyXai?.message).toContain('真实推理健康');
    expect(limitedXai).toMatchObject({ level: 'warning' });
    expect(limitedXai?.message).toContain('真实推理检查');
    expect(expiredXai).toMatchObject({ level: 'error' });
    expect(expiredXai?.message).toContain('真实推理检查');
    expect(logs[logs.length - 1]?.message).toContain('巡检完成：');
  });

  it('uses the same concise summaries while preserving truthful local and server metadata', () => {
    const baseNow = Date.UTC(2026, 6, 23, 12, 0, 0);
    const t = i18n.getFixedT('zh-CN');
    const detail = getDemoCodexInspectionRun(baseNow);
    const localLogs = getDemoCodexInspectionLocalLogs(baseNow, t);
    const localMessages = localLogs.map((log) => log.message);
    const serverMessages = detail.logs.map(
      (log) => toServerInspectionLogViewEntry(log, detail.run, t).message
    );

    expect(serverMessages).toEqual(localMessages);
    expect(localLogs[0]?.detail).toMatchObject({
      triggerType: 'manual',
      triggerKey: 'manual',
    });
    expect(detail.logs[0]?.detail).toMatchObject({
      triggerType: 'scheduled',
      triggerKey: `interval:45:${Math.floor((baseNow - 42 * 60 * 1000) / (45 * 60 * 1000))}`,
    });
    expect(localLogs.slice(1).map((log) => log.detail)).toEqual(
      detail.logs.slice(1).map((log) => log.detail)
    );
  });

  it('returns current xAI billing and inference response shapes by URL and credential', () => {
    const weekly = getDemoApiCallResult({
      authIndex: 'xai-ops-01',
      url: 'https://cli-chat-proxy.grok.com/v1/billing?format=credits',
    });
    const inference = getDemoApiCallResult({
      authIndex: 'xai-ops-01',
      url: 'https://cli-chat-proxy.grok.com/v1/responses',
    });
    const spendingLimit = getDemoApiCallResult({
      authIndex: 'xai-email-user-01',
      url: 'https://cli-chat-proxy.grok.com/v1/responses',
    });
    const expired = getDemoApiCallResult({
      authIndex: 'xai-expired-01',
      url: 'https://cli-chat-proxy.grok.com/v1/responses',
    });

    expect(weekly).toMatchObject({
      status_code: 200,
      body: {
        config: {
          currentPeriod: { type: 'USAGE_PERIOD_TYPE_WEEKLY' },
          creditUsagePercent: 3,
        },
      },
    });
    const deprecatedMonthly = getDemoApiCallResult({
      authIndex: 'xai-ops-01',
      url: 'https://cli-chat-proxy.grok.com/v1/billing',
    });
    expect(deprecatedMonthly).toMatchObject({ status_code: 200, body: { config: {} } });
    expect(inference).toMatchObject({
      status_code: 200,
      body: {
        status: 'completed',
        output: [{ content: [{ type: 'output_text', text: 'OK' }] }],
      },
    });
    expect(spendingLimit).toMatchObject({
      status_code: 402,
      body: { code: 'personal-team-blocked:spending-limit' },
    });
    expect(expired).toMatchObject({
      status_code: 401,
      body: { code: 'invalid_token' },
    });
  });

  it('returns Codex quota, recovery, and expired-auth responses by credential', () => {
    const pro = getDemoApiCallResult({
      authIndex: 'codex-pro-20x-01',
      url: 'https://chatgpt.com/backend-api/wham/usage',
    });
    const recovered = getDemoApiCallResult({
      authIndex: 'codex-fallback-02',
      url: 'https://chatgpt.com/backend-api/wham/usage',
    });
    const expired = getDemoApiCallResult({
      authIndex: 'codex-email-user-01',
      url: 'https://chatgpt.com/backend-api/wham/usage',
    });

    expect(pro).toMatchObject({
      status_code: 200,
      body: { rate_limit: { secondary_window: { used_percent: 96 } } },
    });
    expect(recovered).toMatchObject({
      status_code: 200,
      body: { rate_limit: { primary_window: { used_percent: 24 } } },
    });
    expect(expired).toMatchObject({
      status_code: 401,
      body: { error: { code: 'token_expired' } },
    });
  });

  it('uses Demo Auth File runtime IDs for source-file and expanded-child status scope', () => {
    const base = getDemoCodexInspectionRun().results.find((item) => item.id === 503);
    if (!base) throw new Error('missing demo Codex inspection result 503');
    const source = {
      ...base,
      accountKey: 'shared.json::auth-1',
      fileName: 'shared.json',
      authIndex: 'auth-1',
      accountId: 'account-1',
      displayAccount: 'source@example.com',
    };
    const child = {
      ...source,
      id: 9503,
      accountKey: 'shared.json::auth-2',
      authIndex: 'auth-2',
      accountId: 'account-2',
      displayAccount: 'child@example.com',
    };
    const files = [
      {
        id: 'shared.json',
        name: 'shared.json',
        type: 'codex',
        auth_index: 'auth-1',
        account_id: 'account-1',
      },
      {
        id: 'runtime-auth-2',
        name: 'shared.json',
        type: 'codex',
        auth_index: 'auth-2',
        account_id: 'account-2',
      },
    ];

    expect(isDemoCodexInspectionStatusMutationAmbiguous([source, child], source, files)).toBe(
      false
    );
    expect(isDemoCodexInspectionStatusMutationAmbiguous([source, child], child, files)).toBe(true);
    expect(
      isDemoCodexInspectionStatusMutationAmbiguous(
        [source, child],
        {
          ...child,
          action: 'delete',
        },
        files
      )
    ).toBe(false);
    expect(
      isDemoCodexInspectionStatusMutationAmbiguous(
        [{ ...source, accountKey: 'not-a-runtime-id' }],
        { ...source, accountKey: 'not-a-runtime-id' },
        [files[0]]
      )
    ).toBe(false);
  });

  it('builds complete shared-file action plans with or without a source runtime row', () => {
    const base = getDemoCodexInspectionRun().results.find((item) => item.id === 503);
    if (!base) throw new Error('missing demo Codex inspection result 503');
    const source = {
      ...base,
      id: 9501,
      accountKey: 'shared.json::auth-1',
      fileName: 'shared.json',
      authIndex: 'auth-1',
      accountId: 'account-1',
      displayAccount: 'source@example.com',
      action: 'disable',
    };
    const child = {
      ...source,
      id: 9502,
      accountKey: 'shared.json::auth-2',
      authIndex: 'auth-2',
      accountId: 'account-2',
      displayAccount: 'child@example.com',
    };
    const files = [
      {
        id: 'shared.json',
        name: 'shared.json',
        type: 'codex',
        auth_index: 'auth-1',
        account_id: 'account-1',
      },
      {
        id: 'runtime-auth-2',
        name: 'shared.json',
        type: 'codex',
        auth_index: 'auth-2',
        account_id: 'account-2',
      },
    ];

    const plan = buildDemoCodexInspectionSourceFileStatusActionPlans([source, child], files).get(
      'shared.json'
    );

    expect(plan?.canonicalResultId).toBe(source.id);
    expect(plan?.action).toBe('disable');
    expect(Array.from(plan?.memberResultIds ?? []).sort()).toEqual([source.id, child.id]);
    const pluginFiles = files.map((file, index) => ({
      ...file,
      id: `plugin-runtime-auth-${index + 1}`,
    }));
    const pluginPlan = buildDemoCodexInspectionSourceFileStatusActionPlans(
      [source, child],
      pluginFiles
    ).get('shared.json');
    expect(pluginPlan?.canonicalResultId).toBe(source.id);
    expect(pluginPlan?.action).toBe('disable');
    expect(Array.from(pluginPlan?.memberResultIds ?? []).sort()).toEqual([source.id, child.id]);
    expect(buildDemoCodexInspectionSourceFileStatusActionPlans([source], files).size).toBe(0);
    expect(
      buildDemoCodexInspectionSourceFileStatusActionPlans(
        [source, { ...child, action: 'enable' }],
        files
      ).size
    ).toBe(0);
  });

  it('does not promote workspace IDs or weak snapshots to demo member identity', () => {
    const base = getDemoCodexInspectionRun().results.find((item) => item.id === 503);
    if (!base) throw new Error('missing demo Codex inspection result 503');

    const accountIdIdentity = getDemoCodexInspectionActionIdentityKey({
      ...base,
      fileName: 'shared.json',
      authIndex: undefined,
      accountId: 'shared-identity',
      displayAccount: 'first@example.com',
      accountSnapshot: undefined,
    });
    const accountSnapshotIdentity = getDemoCodexInspectionActionIdentityKey({
      ...base,
      fileName: 'shared.json',
      authIndex: undefined,
      accountId: undefined,
      displayAccount: 'Friendly account',
      accountSnapshot: 'shared-identity',
    });

    expect(accountIdIdentity).toBe('shared.json::-');
    expect(accountSnapshotIdentity).toBe('shared.json::-');
  });

  it('marks executed demo actions as handled in the returned detail', async () => {
    setDemoMode(true);
    await usageServiceApi.runCodexInspection('http://demo.local', 'demo-management-key');

    const response = await usageServiceApi.executeCodexInspectionActions(
      'http://demo.local',
      'demo-management-key',
      1001,
      [503]
    );
    const executed = response.detail.results.find((item) => item.id === 503);

    expect(response.outcomes).toEqual([
      expect.objectContaining({
        resultId: 503,
        action: 'disable',
        status: 'success',
        success: true,
      }),
    ]);
    expect(executed).toMatchObject({
      action: 'disable',
      actionStatus: 'success',
      executedAction: 'disable',
      disabled: true,
    });
    expect(response.detail.logs.slice(-3)).toEqual([
      expect.objectContaining({
        level: 'info',
        message: '手动处理账号开始',
        detail: { requestedCount: 1, actionCount: 1 },
      }),
      expect.objectContaining({
        level: 'success',
        message: '手动处理账号成功',
        detail: expect.objectContaining({ action: 'disable' }),
      }),
      expect.objectContaining({
        level: 'success',
        message: '手动处理账号完成',
        detail: {
          successCount: 1,
          failedCount: 0,
          skippedCount: 0,
          needsReviewCount: 0,
          resultWriteFailedCount: 0,
        },
      }),
    ]);
  });

  it('resets mutable server inspection results when a new demo session starts', async () => {
    setDemoMode(true);
    await usageServiceApi.runCodexInspection('http://demo.local', 'demo-management-key');
    await usageServiceApi.executeCodexInspectionActions(
      'http://demo.local',
      'demo-management-key',
      1001,
      [503]
    );

    const mutated = await usageServiceApi.getCodexInspectionRun(
      'http://demo.local',
      'demo-management-key',
      1001
    );
    expect(mutated.results.find((item) => item.id === 503)).toMatchObject({
      disabled: true,
      actionStatus: 'success',
    });

    resetDemoCodexInspectionRunState();

    const fresh = await usageServiceApi.getCodexInspectionRun(
      'http://demo.local',
      'demo-management-key',
      1001
    );
    expect(fresh.results.find((item) => item.id === 503)).toMatchObject({
      disabled: false,
      actionStatus: 'pending',
    });
    expect(fresh.logs.some((item) => item.message === '手动处理账号完成')).toBe(false);
  });

  it('preserves earlier demo actions and logs across later actions and refreshes', async () => {
    setDemoMode(true);
    await usageServiceApi.runCodexInspection('http://demo.local', 'demo-management-key');

    await usageServiceApi.executeCodexInspectionActions(
      'http://demo.local',
      'demo-management-key',
      1001,
      [503]
    );
    const second = await usageServiceApi.executeCodexInspectionActions(
      'http://demo.local',
      'demo-management-key',
      1001,
      [504]
    );
    const refreshed = await usageServiceApi.getCodexInspectionRun(
      'http://demo.local',
      'demo-management-key',
      1001
    );

    for (const detail of [second.detail, refreshed]) {
      expect(detail.results.find((item) => item.id === 503)).toMatchObject({
        actionStatus: 'success',
        executedAction: 'disable',
        disabled: true,
      });
      expect(detail.results.find((item) => item.id === 504)).toMatchObject({
        actionStatus: 'success',
        executedAction: 'enable',
        disabled: false,
      });
      expect(detail.logs.filter((item) => item.message === '手动处理账号完成')).toHaveLength(2);
    }
  });

  it('matches Manager Server skipped semantics for non-executable and repeated actions', async () => {
    setDemoMode(true);
    await usageServiceApi.runCodexInspection('http://demo.local', 'demo-management-key');

    const nonExecutable = await usageServiceApi.executeCodexInspectionActions(
      'http://demo.local',
      'demo-management-key',
      1001,
      [500, 502]
    );
    expect(nonExecutable.outcomes).toEqual([
      expect.objectContaining({ resultId: 500, action: 'keep', status: 'skipped', success: true }),
      expect.objectContaining({
        resultId: 502,
        action: 'reauth',
        status: 'skipped',
        success: true,
      }),
    ]);
    expect(nonExecutable.detail.results.find((item) => item.id === 500)).toMatchObject({
      actionStatus: 'skipped',
      actionError: '该巡检结果不是可执行动作',
    });
    expect(nonExecutable.detail.logs.slice(-4)).toEqual([
      expect.objectContaining({
        message: '手动处理账号开始',
        detail: { requestedCount: 2, actionCount: 0 },
      }),
      expect.objectContaining({ message: '手动处理账号跳过' }),
      expect.objectContaining({ message: '手动处理账号跳过' }),
      expect.objectContaining({
        level: 'success',
        message: '手动处理账号完成',
        detail: expect.objectContaining({ successCount: 0, skippedCount: 2 }),
      }),
    ]);

    await usageServiceApi.runCodexInspection('http://demo.local', 'demo-management-key');
    await usageServiceApi.executeCodexInspectionActions(
      'http://demo.local',
      'demo-management-key',
      1001,
      [503]
    );
    const repeated = await usageServiceApi.executeCodexInspectionActions(
      'http://demo.local',
      'demo-management-key',
      1001,
      [503]
    );
    expect(repeated.outcomes).toEqual([
      expect.objectContaining({ resultId: 503, status: 'skipped', success: true }),
    ]);
    expect(repeated.detail.results.find((item) => item.id === 503)).toMatchObject({
      actionStatus: 'success',
      executedAction: 'disable',
      disabled: true,
    });
    expect(repeated.detail.logs[repeated.detail.logs.length - 1]).toMatchObject({
      level: 'success',
      detail: expect.objectContaining({ successCount: 0, skippedCount: 1 }),
    });
  });

  it('rejects invalid demo action requests like the Manager Server API', async () => {
    setDemoMode(true);

    await expect(
      usageServiceApi.executeCodexInspectionActions(
        'http://demo.local',
        'demo-management-key',
        1001,
        []
      )
    ).rejects.toMatchObject({
      status: 400,
      message: 'codex inspection action result ids are required',
    });
    await expect(
      usageServiceApi.executeCodexInspectionActions(
        'http://demo.local',
        'demo-management-key',
        9999,
        [503]
      )
    ).rejects.toMatchObject({
      status: 404,
      message: 'codex inspection run not found',
    });
    await expect(
      usageServiceApi.executeCodexInspectionActions(
        'http://demo.local',
        'demo-management-key',
        1001,
        [503],
        [{ resultId: 503, action: 'delete' }]
      )
    ).rejects.toMatchObject({
      status: 400,
      message: 'codex inspection action override is invalid',
    });
  });

  it('drives the local inspection executor through all demo provider scenarios', async () => {
    setDemoMode(true);
    apiClient.setConfig({
      apiBase: 'http://demo.local',
      managementKey: 'demo-management-key',
    });
    const fixtureRun = getDemoCodexInspectionLocalRun();
    const actualLogs: Array<{ level: string; message: string }> = [];
    const t = i18n.getFixedT('zh-CN');
    const result = await inspectCodexAccounts({
      config: normalizeConfigResponse(getDemoRawConfig()),
      apiBase: 'http://demo.local',
      managementKey: 'demo-management-key',
      settings: {
        targetTypes: fixtureRun.settings.targetTypes,
        xaiInferenceEnabled: true,
        xaiInferenceModel: fixtureRun.settings.xaiInferenceModel,
        xaiInferencePrompt: fixtureRun.settings.xaiInferencePrompt,
        xaiInferenceUserAgent: fixtureRun.settings.xaiInferenceUserAgent,
        usedPercentThreshold: fixtureRun.settings.usedPercentThreshold,
        sampleSize: 0,
      },
      onLog: (level, message) => actualLogs.push({ level, message }),
      t,
    });
    const byAuthIndex = new Map(result.results.map((item) => [item.authIndex, item]));
    const expectedTargetCount = fixtureRun.files.filter((file) =>
      ['codex', 'xai'].includes(String(file.provider ?? file.type ?? '').toLowerCase())
    ).length;

    expect(result.results).toHaveLength(expectedTargetCount);
    const fixtureByAuthIndex = new Map(
      fixtureRun.results.map((item) => [item.authIndex, item] as const)
    );
    const resultShape = (item: (typeof result.results)[number]) => ({
      fileName: item.fileName,
      displayAccount: item.displayAccount,
      provider: item.provider,
      disabled: item.disabled,
      status: item.status,
      state: item.state,
      action: item.action,
      statusCode: item.statusCode,
      usedPercent: item.usedPercent,
      isQuota: item.isQuota,
      planType: item.planType,
      errorKind: item.errorKind ?? '',
      quotaWindows: (item.quotaWindows ?? []).map((window) => ({
        id: window.id,
        labelKey: window.labelKey,
        labelParams: window.labelParams,
        usedPercent: window.usedPercent,
        limitWindowSeconds: window.limitWindowSeconds,
      })),
    });

    fixtureByAuthIndex.forEach((fixture, authIndex) => {
      const item = byAuthIndex.get(authIndex);
      expect(item, `missing local result for ${authIndex}`).toBeDefined();
      expect(resultShape(item!)).toEqual(resultShape(fixture));
    });
    expect(byAuthIndex.get('codex-upgrade-demo-01')).toMatchObject({
      action: 'keep',
      statusCode: 200,
      planType: 'free',
    });
    expect(byAuthIndex.get('codex-team-01')).toMatchObject({ action: 'keep', statusCode: 200 });
    expect(byAuthIndex.get('codex-email-user-01')).toMatchObject({
      action: 'reauth',
      statusCode: 401,
      errorKind: 'http_status',
    });
    expect(byAuthIndex.get('codex-pro-20x-01')).toMatchObject({
      action: 'disable',
      statusCode: 200,
      usedPercent: 96,
      isQuota: true,
    });
    expect(byAuthIndex.get('codex-fallback-02')).toMatchObject({
      action: 'enable',
      statusCode: 200,
    });
    expect(byAuthIndex.get('xai-ops-01')).toMatchObject({
      action: 'keep',
      statusCode: 200,
      errorKind: 'inference_healthy',
      disabled: true,
    });
    expect(byAuthIndex.get('xai-email-user-01')).toMatchObject({
      action: 'disable',
      statusCode: 402,
      errorKind: 'spending_limit',
    });
    expect(byAuthIndex.get('xai-expired-01')).toMatchObject({
      action: 'reauth',
      statusCode: 401,
      usedPercent: 12,
      errorKind: 'auth_invalid',
    });

    const fixtureLogs = getDemoCodexInspectionLocalLogs(Date.now(), t).map(
      ({ level, message }) => ({ level, message })
    );
    const sortLogs = (items: Array<{ level: string; message: string }>) =>
      [...items].sort((left, right) =>
        `${left.level}\u0000${left.message}`.localeCompare(`${right.level}\u0000${right.message}`)
      );
    expect(sortLogs(fixtureLogs)).toEqual(sortLogs(actualLogs));
  });
});
