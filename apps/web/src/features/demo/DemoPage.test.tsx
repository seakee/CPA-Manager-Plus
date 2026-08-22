import { afterEach, describe, expect, it, vi } from 'vitest';
import { createAppRoutes } from '@/app/appRoutes';
import { CODEX_INSPECTION_LAST_RUN_STORAGE_KEY } from '@/features/monitoring/model/codexInspectionStorage';
import { CODEX_INSPECTION_SETTINGS_STORAGE_KEY } from '@/features/monitoring/model/codexInspectionSettings';
import { accountQuotaSnapshotApi } from '@/services/api/usageService';
import {
  getDemoAccountActionCandidates,
  getDemoAccountHistory,
  getDemoAccountWindowUsage,
  getDemoAuthFiles,
  getDemoCodexInspectionRun,
  getDemoDashboardSummary,
  getDemoErrorLogsResponse,
  getDemoLatestVersion,
  getDemoManagerLatestRelease,
  getDemoManagerConfig,
  getDemoHeaderSnapshots,
  getDemoMonitoringAnalytics,
  getDemoPluginStore,
  getDemoQuotaCooldowns,
  getDemoQuotaStoreState,
  getDemoRawConfig,
} from './demoFixtures';
import {
  DEMO_ROUTE_BASE,
  getDemoServerBuildDate,
  ensureRouteBasePathname,
  getDemoLogoutHash,
  getDemoLogoutPath,
  isDemoMode,
  prefixRouteBase,
  setDemoMode,
  stripRouteBase,
} from './demoMode';
import { installDemoInspectionState } from './DemoPage';

const createMemoryStorage = () => {
  const values = new Map<string, string>();
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
    removeItem: (key: string) => values.delete(key),
    clear: () => values.clear(),
    key: (index: number) => Array.from(values.keys())[index] ?? null,
    get length() {
      return values.size;
    },
  };
};

describe('DemoPage', () => {
  afterEach(() => {
    setDemoMode(false);
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('keeps demo routes under the demo prefix while matching real routes internally', () => {
    expect(stripRouteBase('/demo', DEMO_ROUTE_BASE)).toBe('/');
    expect(stripRouteBase('/demo/config', DEMO_ROUTE_BASE)).toBe('/config');
    expect(stripRouteBase('/demo/monitoring?tab=events', DEMO_ROUTE_BASE)).toBe(
      '/monitoring?tab=events'
    );

    expect(prefixRouteBase('/', DEMO_ROUTE_BASE)).toBe('/demo');
    expect(prefixRouteBase('/config', DEMO_ROUTE_BASE)).toBe('/demo/config');
    expect(prefixRouteBase('/monitoring/account-actions', DEMO_ROUTE_BASE)).toBe(
      '/demo/monitoring/account-actions'
    );

    expect(ensureRouteBasePathname('/', DEMO_ROUTE_BASE)).toBe('/demo');
    expect(ensureRouteBasePathname('/config', DEMO_ROUTE_BASE)).toBe('/demo/config');
    expect(ensureRouteBasePathname('/demo/config', DEMO_ROUTE_BASE)).toBe('/demo/config');
  });

  it('keeps demo site routing isolated from the real login panel', () => {
    const demoChildren = createAppRoutes()[0]?.children ?? [];
    const demoPaths = demoChildren.map((route) => route.path ?? '(index)');

    expect(demoPaths).toEqual(['(index)', '/demo/*', '*']);
    expect(demoPaths).not.toContain('/login');
    expect(demoPaths).not.toContain('/*');
  });

  it('keeps demo logout inside the demo site', () => {
    expect(getDemoLogoutPath()).toBe('/demo');
    expect(getDemoLogoutPath(DEMO_ROUTE_BASE)).toBe('/demo');
    expect(getDemoLogoutHash()).toBe('#/demo');
    expect(getDemoLogoutHash(DEMO_ROUTE_BASE)).toBe('#/demo');
    expect(getDemoLogoutHash('/demo/')).toBe('#/demo');
    expect(getDemoLogoutHash()).not.toBe('#/login');
  });

  it('recognizes deep demo hash routes before demo stores are mounted', () => {
    vi.stubGlobal('window', {
      location: {
        hash: '#/demo/plugins',
        pathname: '/',
      },
    });

    expect(isDemoMode()).toBe(true);
  });

  it('keeps normal hash routes out of demo mode', () => {
    vi.stubGlobal('window', {
      location: {
        hash: '#/dashboard',
        pathname: '/',
      },
    });

    expect(isDemoMode()).toBe(false);
  });

  it('returns the xAI rolling response-body quota snapshot in demo mode', async () => {
    const generatedAtMs = Date.parse('2026-08-04T12:00:00Z');
    setDemoMode(true);

    const response = await accountQuotaSnapshotApi.query(
      '',
      undefined,
      [
        {
          row_key: 'xai-ops.json\u0000xai-ops-01',
          provider: 'xai',
          account: { auth_index: 'xai-ops-01' },
        },
        {
          row_key: 'xai-email-user.json\u0000xai-email-user-01',
          provider: 'xai',
          account: { auth_index: 'xai-email-user-01' },
        },
      ],
      { nowMs: generatedAtMs }
    );

    expect(response.generated_at_ms).toBe(generatedAtMs);
    expect(response.items[0]?.windows).toEqual([
      expect.objectContaining({
        provider_window_id: 'included-free-rolling-24h',
        window_kind: 'rolling_24h',
        window_mode: 'rolling',
        model_scope_kind: 'models',
        model_ids: ['grok-4.5-build-free'],
        source: 'response_body',
        boundary_accuracy: 'estimated',
        duration_seconds: 86_400,
        stale: false,
      }),
    ]);
    expect(response.items[1]?.windows).toEqual([]);
  });

  it('returns Codex lifecycle cycles and filters inactive periods on demand', async () => {
    const generatedAtMs = Date.parse('2026-08-04T12:00:00Z');
    setDemoMode(true);
    const account = {
      row_key: 'codex-team-01.json\u0000codex-team-01',
      provider: 'codex',
      account: { auth_index: 'codex-team-01' },
    };

    const activeOnly = await accountQuotaSnapshotApi.query('', undefined, [account], {
      nowMs: generatedAtMs,
    });
    expect(activeOnly.items[0]?.windows.map((window) => window.provider_window_id)).toEqual([
      'five-hour',
      'weekly',
    ]);

    const withInactive = await accountQuotaSnapshotApi.query('', undefined, [account], {
      nowMs: generatedAtMs,
      includeInactive: true,
    });
    expect(withInactive.items[0]?.windows).toHaveLength(3);
    expect(withInactive.items[0]?.windows[0]).toMatchObject({
      provider_window_id: 'five-hour',
      activation_generation: 2,
      relationship_kind: 'concurrent_subwindow',
      current_cycle: { parent_cycle_id: 302 },
    });
    expect(withInactive.items[0]?.windows[1]?.previous_cycle).toMatchObject({
      end_reason: 'early_reset',
      forecast_eligible: false,
    });
    expect(withInactive.items[0]?.windows[2]).toMatchObject({
      provider_window_id: 'monthly',
      availability: 'inactive',
      stale: true,
    });
  });

  it('does not infer demo mode from the deployment pathname without a demo hash route', () => {
    vi.stubGlobal('window', {
      location: {
        hash: '',
        pathname: '/demo/management.html',
      },
    });

    expect(isDemoMode()).toBe(false);
  });

  it('keeps demo mock data free of historical analysis labels', () => {
    const visibleData = JSON.stringify([
      getDemoRawConfig(),
      getDemoAuthFiles(),
      getDemoPluginStore(),
      getDemoManagerConfig(),
      getDemoDashboardSummary(),
      getDemoMonitoringAnalytics(),
    ]);
    const historicalAnalysisLabel = ['cc', 'switch'].join('-');

    expect(visibleData.toLowerCase()).not.toContain(historicalAnalysisLabel);
  });

  it('does not expose a readable CPA Management Key in demo Manager config', () => {
    const connection = getDemoManagerConfig().config.cpaConnection;

    expect(connection).toEqual({
      cpaBaseUrl: expect.any(String),
      managementKeyConfigured: true,
    });
    expect(JSON.stringify(connection)).not.toContain('demo-cpa-management-key');
  });

  it('fills accounts with realistic OAuth login data across statuses and quota providers', () => {
    const authFiles = getDemoAuthFiles();
    const fileNames = new Set(authFiles.files.map((file) => file.name));
    const providers = new Set(authFiles.files.map((file) => String(file.provider ?? file.type)));
    const providerCounts = authFiles.files.reduce<Record<string, number>>((result, file) => {
      const provider = String(file.provider ?? file.type);
      result[provider] = (result[provider] ?? 0) + 1;
      return result;
    }, {});
    const quota = getDemoQuotaStoreState();
    const analytics = getDemoMonitoringAnalytics();
    const historyTargetString = (value: unknown) => {
      if (typeof value === 'string') return value;
      if (typeof value === 'number' && Number.isFinite(value)) return String(value);
      return undefined;
    };
    const accountHistory = getDemoAccountHistory({
      accounts: authFiles.files.map((file) => ({
        row_key: String(file.id ?? `${file.name}:${historyTargetString(file.authIndex) ?? '-'}`),
        account_snapshot:
          historyTargetString(file.account_snapshot) ??
          historyTargetString(file.account) ??
          historyTargetString(file.email),
        auth_label_snapshot: historyTargetString(file.label) ?? historyTargetString(file.note),
        auth_file_snapshot: file.name,
        auth_provider_snapshot:
          historyTargetString(file.provider) ?? historyTargetString(file.type),
        auth_project_id_snapshot:
          historyTargetString(file.project_id) ?? historyTargetString(file.projectId),
        source: file.name,
        auth_index: historyTargetString(file.authIndex),
      })),
    });
    const oauthProviders = ['antigravity', 'claude', 'codex', 'kimi', 'xai'];
    const analyticsProviderList = [
      'antigravity',
      'claude',
      'codex',
      'deepseek',
      'gemini',
      'kimi',
      'openai',
      'xai',
    ];
    const nonOauthFiles = [
      'gemini-prod-01.json',
      'vertex-regional-01.json',
      'gemini-batch-02.json',
      'deepseek-ops-01.json',
      'kuai-auth-1.json',
      'kuai-auth-2.json',
      'anyrouter-auth-1.json',
    ];
    const visibleAccountText = authFiles.files
      .map((file) =>
        [file.name, file.account, file.email, file.label, file.note, file.account_snapshot].join(
          ' '
        )
      )
      .join('\n');
    const cooldownKeys = new Set(
      getDemoQuotaCooldowns().map((item) => `${item.authFileName}:${item.authIndex ?? '-'}`)
    );
    const headerFiles = new Set(
      getDemoHeaderSnapshots()
        .items.map((item) => item.auth_file_snapshot)
        .filter(Boolean)
    );
    const inspectionFiles = new Set(
      getDemoCodexInspectionRun().results.map((item) => item.fileName)
    );
    const analyticsProviders = new Set(
      [
        ...(analytics.account_stats ?? []).map((item) => item.auth_provider_snapshot),
        ...(analytics.credential_stats ?? []).map((item) => item.auth_provider_snapshot),
        ...(analytics.events?.items ?? []).map((item) => item.auth_provider_snapshot),
      ].filter((provider): provider is string => Boolean(provider))
    );

    expect(authFiles.total).toBe(authFiles.files.length);
    expect(authFiles.files.length).toBe(23);
    expect(authFiles.files.every((file) => typeof file.id === 'string' && file.id.length > 0)).toBe(
      true
    );
    expect(new Set(authFiles.files.map((file) => file.id)).size).toBe(authFiles.files.length);
    expect(visibleAccountText).not.toMatch(/\bui[-.]/i);
    expect(
      authFiles.files.every((file) =>
        Boolean(file.account || file.email || file.label || file.note)
      )
    ).toBe(true);
    expect(
      authFiles.files.every((file) =>
        (file.recent_requests ?? []).some((bucket) => bucket.success + bucket.failed > 0)
      )
    ).toBe(true);
    expect(Array.from(providers).sort()).toEqual([...oauthProviders, 'openai'].sort());
    oauthProviders.forEach((provider) => {
      expect(providerCounts[provider]).toBeGreaterThanOrEqual(3);
    });
    expect(providerCounts.openai).toBe(1);
    expect(Array.from(analyticsProviders).sort()).toEqual(analyticsProviderList);
    expect([...(analytics.filter_options?.providers ?? [])].sort()).toEqual(analyticsProviderList);
    expect(accountHistory.checkpoint.pending).toBe(false);
    expect(accountHistory.items).toHaveLength(authFiles.files.length);
    expect(accountHistory.items.every((item) => item.matched)).toBe(true);
    expect(
      accountHistory.items.every((item) => {
        const recentRequests = item.recent_requests ?? [];
        return (
          recentRequests.length > 0 &&
          recentRequests.length <= 10 &&
          JSON.stringify(item.latest_request) === JSON.stringify(recentRequests[0]) &&
          recentRequests.every(
            (request, index) =>
              index === 0 || recentRequests[index - 1].timestamp_ms > request.timestamp_ms
          )
        );
      })
    ).toBe(true);
    const recentRequestLengths = new Set(
      accountHistory.items.map((item) => item.recent_requests?.length ?? 0)
    );
    expect(Array.from(recentRequestLengths)).toEqual(expect.arrayContaining([1, 2, 3, 5, 10]));
    const recentRequests = accountHistory.items.flatMap((item) => item.recent_requests ?? []);
    expect(recentRequests.some((request) => !request.failed)).toBe(true);
    expect(recentRequests.some((request) => request.failed)).toBe(true);
    expect(
      Array.from(
        new Set(
          recentRequests
            .filter((request) => request.failed)
            .map((request) => request.fail_status_code)
        )
      )
    ).toEqual(expect.arrayContaining([401, 429, 500, 503]));
    expect(
      accountHistory.items.every(
        (item) =>
          item.total_requests > 0 &&
          item.total_tokens > 0 &&
          item.total_cost > 0 &&
          item.success_rate !== null &&
          item.sync_status === 'ready'
      )
    ).toBe(true);
    expect(
      accountHistory.items.find((item) => item.row_key === 'codex-team-01.json')
    ).toMatchObject({
      total_requests: 5200,
      total_tokens: 4_220_000,
      total_cost: 88.1,
    });
    expect(Array.from(fileNames).sort()).toEqual(
      [
        'antigravity-daily-exhausted.json',
        'antigravity-builder.json',
        'antigravity-free-weekly.json',
        'antigravity-monthly-low.json',
        'antigravity-pro-matrix.json',
        'claude-extra-usage-03.json',
        'claude-research-02.json',
        'claude-team-01.json',
        'codex-email-user.json',
        'codex-expired-oauth-03.json',
        'codex-fallback-02.json',
        'codex-pro-20x-01.json',
        'codex-team-01.json',
        'codex-upgrade-demo.json',
        'kimi-coding.json',
        'kimi-exhausted.json',
        'kimi-healthy.json',
        'openai-support-02.json',
        'xai-email-user.json',
        'xai-expired.json',
        'xai-ops.json',
        'xai-payg-buffer.json',
        'xai-payg-cap.json',
      ].sort()
    );
    nonOauthFiles.forEach((fileName) => expect(fileNames).not.toContain(fileName));

    const indexQuotaByFileName = <TState extends { authFileName?: string }>(
      record: Record<string, TState>
    ) => new Map(Object.values(record).map((state) => [state.authFileName, state]));
    const codexQuotaByFileName = indexQuotaByFileName(quota.codexQuota);
    const claudeQuotaByFileName = indexQuotaByFileName(quota.claudeQuota);
    const antigravityQuotaByFileName = indexQuotaByFileName(quota.antigravityQuota);
    const kimiQuotaByFileName = indexQuotaByFileName(quota.kimiQuota);
    const xaiQuotaByFileName = indexQuotaByFileName(quota.xaiQuota);
    expect(Array.from(codexQuotaByFileName.keys())).toEqual(
      expect.arrayContaining([
        'codex-team-01.json',
        'codex-fallback-02.json',
        'codex-expired-oauth-03.json',
      ])
    );
    expect(authFiles.files.find((file) => file.name === 'codex-email-user.json')).toMatchObject({
      id_token: {
        plan_type: 'plus',
        chatgpt_subscription_active_until: expect.any(String),
      },
    });
    expect(codexQuotaByFileName.get('codex-team-01.json')?.subscriptionActiveUntil).toEqual(
      expect.any(String)
    );
    expect(codexQuotaByFileName.get('codex-team-01.json')?.windows[0]).toMatchObject({
      resetAtMs: expect.any(Number),
      resetAccuracy: 'estimated',
    });
    expect(codexQuotaByFileName.get('codex-expired-oauth-03.json')?.status).toBe('error');
    expect(codexQuotaByFileName.get('codex-expired-oauth-03.json')?.errorStatus).toBe(401);
    expect(claudeQuotaByFileName.get('claude-research-02.json')?.windows).toHaveLength(3);
    expect(claudeQuotaByFileName.get('claude-research-02.json')?.windows[0]).toMatchObject({
      resetAtMs: expect.any(Number),
      resetAccuracy: 'estimated',
    });
    expect(claudeQuotaByFileName.get('claude-extra-usage-03.json')?.extraUsage?.is_enabled).toBe(
      true
    );
    expect(antigravityQuotaByFileName.get('antigravity-builder.json')?.groups).toHaveLength(2);
    expect(
      antigravityQuotaByFileName.get('antigravity-builder.json')?.groups[0]?.buckets
    ).toHaveLength(2);
    expect(
      antigravityQuotaByFileName.get('antigravity-daily-exhausted.json')?.groups[0]?.buckets[0]
        ?.remainingFraction
    ).toBe(0);
    expect(
      antigravityQuotaByFileName.get('antigravity-monthly-low.json')?.groups[1]?.buckets[1]
        ?.remainingFraction
    ).toBe(0.08);
    expect(antigravityQuotaByFileName.get('antigravity-free-weekly.json')?.subscription?.plan).toBe(
      'free'
    );
    expect(antigravityQuotaByFileName.get('antigravity-free-weekly.json')?.groups).toHaveLength(2);
    expect(
      antigravityQuotaByFileName
        .get('antigravity-free-weekly.json')
        ?.groups.every(
          (group) => group.buckets.length === 1 && group.buckets[0]?.window === 'weekly'
        )
    ).toBe(true);
    expect(
      antigravityQuotaByFileName.get('antigravity-pro-matrix.json')?.groups[1]?.buckets[0]
    ).toMatchObject({
      window: '5h',
      remainingFraction: 0.11,
    });
    expect(kimiQuotaByFileName.get('kimi-coding.json')?.rows[0]?.used).toBe(214);
    expect(kimiQuotaByFileName.get('kimi-coding.json')?.rows[0]).toMatchObject({
      resetAtMs: expect.any(Number),
      resetAccuracy: 'estimated',
    });
    expect(kimiQuotaByFileName.get('kimi-healthy.json')?.rows[0]?.used).toBe(320);
    expect(kimiQuotaByFileName.get('kimi-exhausted.json')?.rows[1]?.used).toBe(200);
    expect(xaiQuotaByFileName.get('xai-ops.json')?.billing?.periodType).toBe('weekly');
    expect(xaiQuotaByFileName.get('xai-ops.json')?.billing?.usagePercent).toBe(42);
    expect(xaiQuotaByFileName.get('xai-ops.json')?.billing?.monthlyLimitCents).toBeNull();
    expect(xaiQuotaByFileName.get('xai-ops.json')?.billing?.usedPercent).toBeNull();
    expect(xaiQuotaByFileName.get('xai-payg-buffer.json')?.billing?.usedPercent).toBe(100);
    expect(xaiQuotaByFileName.get('xai-payg-buffer.json')?.billing?.onDemandUsedPercent).toBe(26);
    expect(xaiQuotaByFileName.get('xai-payg-cap.json')?.billing?.onDemandUsedPercent).toBe(100);

    expect(Array.from(cooldownKeys)).toEqual(
      expect.arrayContaining(['codex-fallback-02.json:codex-fallback-02'])
    );
    expect(headerFiles).toContain('codex-fallback-02.json');
    expect(Array.from(inspectionFiles)).toEqual(
      expect.arrayContaining([
        'codex-fallback-02.json',
        'codex-email-user.json',
        'xai-expired.json',
      ])
    );
  });

  it('prefers structured account-history identity over a stale opaque account key', () => {
    const response = getDemoAccountHistory({
      accounts: [
        {
          row_key: 'platform-team-row',
          account_key: 'stale-account-key',
          auth_file_snapshot: 'codex-team-01.json',
          auth_provider_snapshot: 'codex',
          auth_index: 'codex-team-01',
          account_snapshot: 'Platform Team',
          source: 'codex-team-01.json',
        },
      ],
    });

    expect(response.items[0]).toMatchObject({
      row_key: 'platform-team-row',
      matched: true,
      total_requests: 5200,
      total_tokens: 4_220_000,
      total_cost: 88.1,
    });
    expect(response.items[0]?.account_key).not.toBe('stale-account-key');
  });

  it('echoes stable window identities for quota-tab current and previous usage', () => {
    const response = getDemoAccountWindowUsage({
      windows: [
        {
          request_key: 'platform-team-row\u0000rate_limit:weekly\u0000all\u0000previous',
          row_key: 'platform-team-row',
          window_key: 'weekly',
          provider_window_id: 'rate_limit:weekly',
          period: 'previous',
          from_ms: Date.now() - 14 * 24 * 60 * 60 * 1000,
          to_ms: Date.now() - 7 * 24 * 60 * 60 * 1000,
          model_scope: { kind: 'all' },
          account_snapshot: 'Platform Team',
          auth_index: 'codex-team-01',
          source: 'codex-team-01.json',
        },
      ],
    });

    expect(response.items[0]).toMatchObject({
      request_key: 'platform-team-row\u0000rate_limit:weekly\u0000all\u0000previous',
      row_key: 'platform-team-row',
      window_key: 'weekly',
      provider_window_id: 'rate_limit:weekly',
      period: 'previous',
      matched: true,
      scope_match_status: 'complete',
      unmatched_requests: 0,
    });
  });

  it('fills the dashboard request health timeline with real dashboard granularity', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-06-29T10:00:00+08:00'));

    const timeline = getDemoDashboardSummary().today_request_health_timeline;

    expect(timeline).toBeDefined();
    if (!timeline) throw new Error('missing demo request health timeline');

    expect(timeline.bucket_ms).toBe(10 * 60 * 1000);
    expect(timeline.points).toHaveLength(144);
    const tones = new Set(timeline.points.map((point) => point.tone));
    expect(tones.has('empty')).toBe(true);
    expect(tones.has('good')).toBe(true);
    expect(tones.has('warn')).toBe(true);
    expect(tones.has('bad')).toBe(true);
    expect(tones.has('future')).toBe(true);
  });

  it('fills usage analytics and request monitoring tabs with complete demo pages', () => {
    const demoAuthCount = getDemoAuthFiles().files.length;
    const firstPage = getDemoMonitoringAnalytics({
      from_ms: 0,
      to_ms: Date.now(),
      include: {
        events_page: { limit: 10 },
        recent_failures: 1,
        drilldown_preview: { from_ms: 0, to_ms: Date.now(), limit: 8 },
      },
    });

    expect(firstPage.model_stats?.length).toBeGreaterThanOrEqual(8);
    expect(firstPage.account_stats?.length).toBeGreaterThanOrEqual(demoAuthCount);
    expect(firstPage.api_key_stats?.length).toBe(demoAuthCount);
    expect(firstPage.credential_stats?.length).toBe(demoAuthCount);
    expect(firstPage.credential_timeline?.length).toBe(
      Math.min(firstPage.credential_stats?.length ?? 0, 10) * 7
    );
    expect(firstPage.heatmap).toHaveLength(168);
    expect(firstPage.heatmap?.some((point) => point.calls > 0)).toBe(true);
    expect(firstPage.events?.items).toHaveLength(10);
    expect(firstPage.events?.has_more).toBe(true);
    expect(firstPage.recent_failures).toHaveLength(1);
    expect(
      new Set(firstPage.events?.items.map((event) => event.api_key_hash)).size
    ).toBeGreaterThanOrEqual(8);

    const secondPage = getDemoMonitoringAnalytics({
      from_ms: 0,
      to_ms: Date.now(),
      include: {
        events_page: { limit: 10, before_ms: firstPage.events?.next_before_ms },
      },
    });
    const firstHashes = new Set(firstPage.events?.items.map((event) => event.event_hash));

    expect(secondPage.events?.items).toHaveLength(10);
    expect(secondPage.events?.items.every((event) => !firstHashes.has(event.event_hash))).toBe(
      true
    );
  });

  it('keeps morning monitoring events inside the request clock and exposes model identities', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-06-29T10:00:00+08:00'));
    const requestNow = Date.now();
    const rangeEnd = requestNow - 2 * 60 * 1000;

    const page = getDemoMonitoringAnalytics({
      from_ms: rangeEnd - 24 * 60 * 60 * 1000,
      to_ms: rangeEnd,
      now_ms: requestNow,
      include: { events_page: { limit: 20 } },
    });
    const events = page.events?.items ?? [];
    const maxEvent = events.find(
      (event) => event.event_hash === 'demo-event-deepseek-reasoning-max'
    );
    const lowEvent = events.find(
      (event) => event.event_hash === 'demo-event-deepseek-reasoning-low'
    );
    const regionEvent = events.find(
      (event) => event.event_hash === 'demo-event-deepseek-region-alias'
    );

    expect(page.generated_at_ms).toBe(rangeEnd);
    expect(events.length).toBeGreaterThan(0);
    expect(events.every((event) => event.timestamp_ms <= requestNow)).toBe(true);
    expect(events.every((event) => event.timestamp_ms <= rangeEnd)).toBe(true);
    expect(maxEvent).toMatchObject({
      model: 'deepseek-chat(max)',
      analytics_model: 'deepseek-chat',
      requested_model: 'deepseek-chat(max)',
      resolved_model: 'deepseek-chat-202608',
      reasoning_effort: 'max',
    });
    expect(lowEvent).toMatchObject({
      model: 'deepseek-chat(low)',
      analytics_model: 'deepseek-chat',
      requested_model: 'deepseek-chat(low)',
      resolved_model: 'deepseek-chat-202608',
      reasoning_effort: 'low',
    });
    expect(regionEvent).toMatchObject({
      model: 'deepseek-chat(region-us)',
      analytics_model: 'deepseek-chat(region-us)',
      requested_model: 'deepseek-chat(region-us)',
      resolved_model: 'deepseek-chat(region-us)',
    });
  });

  it('provides stable reason codes for localized account diagnostics', () => {
    const candidates = getDemoAccountActionCandidates().items;

    expect(
      candidates.find((candidate) => candidate.authFileName === 'codex-fallback-02.json')
        ?.reasonCode
    ).toBe('authentication_review');
    expect(
      candidates.find((candidate) => candidate.authFileName === 'codex-expired-oauth-03.json')
        ?.reasonCode
    ).toBe('token_revoked');
  });

  it('filters credential overview analytics instead of returning the global demo summary', () => {
    const request = {
      from_ms: Date.now() - 7 * 24 * 60 * 60 * 1000,
      to_ms: Date.now(),
      filters: {
        auth_files: ['antigravity-daily-exhausted.json'],
        auth_indices: ['antigravity-daily-02'],
      },
      include: {
        summary: true,
        account_stats: true,
      },
    };
    const filtered = getDemoMonitoringAnalytics(request);

    expect(filtered.summary).toMatchObject({
      total_calls: 540,
      success_calls: 496,
      failure_calls: 44,
      total_tokens: 460_000,
      total_cost: 8.7,
    });
    expect(filtered.credential_stats?.map((row) => row.auth_file_snapshot)).toEqual([
      'antigravity-daily-exhausted.json',
    ]);
    expect(filtered.account_stats).toHaveLength(1);
    expect(filtered.account_stats?.[0]?.auth_indices).toContain('antigravity-daily-02');

    const empty = getDemoMonitoringAnalytics({
      ...request,
      filters: {
        auth_files: ['missing.json'],
        auth_indices: ['missing'],
      },
    });
    expect(empty.summary).toMatchObject({
      total_calls: 0,
      success_calls: 0,
      failure_calls: 0,
      total_tokens: 0,
      total_cost: 0,
    });
    expect(empty.account_stats).toEqual([]);
    expect(empty.credential_stats).toEqual([]);

    const byCredentialId = getDemoMonitoringAnalytics({
      ...request,
      filters: {
        credential_ids: ['codex-team-01.json'],
      },
    });
    expect(byCredentialId.summary).toMatchObject({
      total_calls: 5200,
      total_tokens: 4_220_000,
      total_cost: 88.1,
    });
    expect(byCredentialId.account_stats).toHaveLength(1);
    expect(byCredentialId.account_stats?.[0]?.auth_indices).toEqual(['codex-team-01']);
    expect(byCredentialId.credential_stats?.[0]?.id).toBe('codex-team-01.json');
    expect(byCredentialId.account_stats?.[0]?.last_seen_ms).toBe(
      byCredentialId.credential_stats?.[0]?.last_seen_ms
    );

    const authIndexIsNotCredentialId = getDemoMonitoringAnalytics({
      ...request,
      filters: {
        credential_ids: ['codex-team-01'],
      },
    });
    expect(authIndexIsNotCredentialId.credential_stats).toEqual([]);
    expect(authIndexIsNotCredentialId.account_stats).toEqual([]);

    const scopedEvents = getDemoMonitoringAnalytics({
      from_ms: 0,
      to_ms: Date.now(),
      filters: request.filters,
      include: {
        events_page: { limit: 50 },
      },
    });
    expect(scopedEvents.events?.items.length).toBeGreaterThan(0);
    expect(
      scopedEvents.events?.items.every(
        (event) =>
          event.auth_file_snapshot === 'antigravity-daily-exhausted.json' &&
          event.auth_index === 'antigravity-daily-02'
      )
    ).toBe(true);
    expect(scopedEvents.events?.total_count).toBe(scopedEvents.events?.items.length);
  });

  it('returns exact API key trend fixtures for selected client keys', () => {
    const page = getDemoMonitoringAnalytics({
      from_ms: 1,
      to_ms: Date.now(),
      filters: {
        api_key_hashes: ['hash_research_shared', 'hash_codex_team'],
      },
      include: {
        api_key_timeline: true,
      },
    });
    const timeline = page.timeline;
    const apiKeyTimeline = page.api_key_timeline;
    if (!timeline || !apiKeyTimeline) throw new Error('missing demo API key timeline');
    const firstBucket = timeline[0];
    const missingCodexBucket = timeline[3];
    if (!firstBucket || !missingCodexBucket) throw new Error('missing demo timeline buckets');

    expect([...new Set(apiKeyTimeline.map((point) => point.api_key_hash))].sort()).toEqual([
      'hash_codex_team',
      'hash_research_shared',
    ]);
    expect(apiKeyTimeline).toHaveLength(timeline.length * 2 - 2);

    const firstResearchPoint = apiKeyTimeline.find(
      (point) =>
        point.api_key_hash === 'hash_research_shared' && point.bucket_ms === firstBucket.bucket_ms
    );
    if (!firstResearchPoint) throw new Error('missing first research API key bucket');
    expect(firstResearchPoint).toMatchObject({
      calls: Math.round(firstBucket.calls * 0.36),
      total_tokens: Math.round(firstBucket.tokens * 0.39),
    });
    expect(firstResearchPoint.success + firstResearchPoint.failure).toBe(firstResearchPoint.calls);
    expect(
      apiKeyTimeline.some(
        (point) =>
          point.api_key_hash === 'hash_codex_team' &&
          point.bucket_ms === missingCodexBucket.bucket_ms
      )
    ).toBe(false);
  });

  it('provides xAI quota exhaustion, successful rate-limit, and cooldown fixtures for UI acceptance', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-07-21T10:25:05.000+08:00'));

    const analytics = getDemoMonitoringAnalytics({
      from_ms: 0,
      to_ms: Date.now(),
      include: { events_page: { limit: 10 } },
    });
    const exhausted = analytics.events?.items.find(
      (event) => event.event_hash === 'demo-event-xai-free-usage-exhausted'
    );
    const successfulRateLimit = analytics.events?.items.find(
      (event) => event.event_hash === 'demo-event-xai-rate-limit-success'
    );
    const xaiCooldown = getDemoQuotaCooldowns().find(
      (cooldown) => cooldown.authFileName === 'xai-ops.json'
    );
    const xaiAuthFile = getDemoAuthFiles().files.find((file) => file.name === 'xai-ops.json');
    const xaiSnapshots = getDemoHeaderSnapshots().items.filter((snapshot) =>
      snapshot.event_hash.startsWith('demo-event-xai-')
    );

    expect(exhausted).toMatchObject({
      failed: true,
      fail_status_code: 429,
      auth_file_snapshot: 'xai-ops.json',
      auth_provider_snapshot: 'xai',
      header_error_code: 'subscription:free-usage-exhausted',
      response_metadata: {
        errors: { should_retry: true },
        provider_usage: {
          provider: 'xai',
          state: 'exhausted',
          actual: 1_024_413,
          limit: 1_000_000,
          overage: 24_413,
          window_kind: 'rolling_24h',
          recover_at_estimated: true,
        },
        data_policy: { retention_mode: 'zdr', zero_retention: true },
      },
    });
    expect(successfulRateLimit).toMatchObject({
      failed: false,
      auth_file_snapshot: 'xai-email-user.json',
      response_metadata: {
        rate_limit: { requests: { limit: 21, remaining: 18 } },
        data_policy: { retention_mode: 'zdr', zero_retention: true },
      },
    });
    expect(xaiCooldown).toMatchObject({
      provider: 'xai',
      owner: 'cpamp_xai_free_usage',
      reasonCode: 'xai_free_usage_exhausted',
      windowKind: 'rolling_24h',
      evidence: {
        actual: 1_024_413,
        limit: 1_000_000,
        recover_at_estimated: true,
      },
    });
    expect(xaiCooldown?.evidence?.recover_at_ms).toBe(xaiCooldown?.recoverAtMs);
    expect(xaiAuthFile).toMatchObject({ disabled: true, status: 'cooldown' });
    expect(xaiSnapshots.map((snapshot) => snapshot.event_hash)).toEqual([
      'demo-event-xai-free-usage-exhausted',
      'demo-event-xai-rate-limit-success',
    ]);
  });

  it('keeps visible demo dates relative to the current day', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-06-29T10:00:00+08:00'));

    expect(getDemoServerBuildDate()).toBe('2026-06-29');
    expect(getDemoLatestVersion().buildDate).toBe('2026-06-29');
    expect(getDemoErrorLogsResponse().files.map((file) => file.name)).toEqual([
      'request-errors-2026-06-29.jsonl',
      'request-errors-2026-06-28.jsonl',
    ]);
    expect(new Date(getDemoManagerLatestRelease().published_at).getTime()).toBe(
      new Date(2026, 5, 29).getTime()
    );

    vi.setSystemTime(new Date('2026-06-30T10:00:00+08:00'));

    expect(getDemoServerBuildDate()).toBe('2026-06-30');
    expect(getDemoLatestVersion().buildDate).toBe('2026-06-30');
    expect(getDemoErrorLogsResponse().files.map((file) => file.name)).toEqual([
      'request-errors-2026-06-30.jsonl',
      'request-errors-2026-06-29.jsonl',
    ]);
    expect(new Date(getDemoManagerLatestRelease().published_at).getTime()).toBe(
      new Date(2026, 5, 30).getTime()
    );
  });

  it('installs inspection demo state and restores the existing local state on exit', () => {
    const storage = createMemoryStorage();
    storage.setItem(CODEX_INSPECTION_LAST_RUN_STORAGE_KEY, 'real-last-run');
    storage.setItem(CODEX_INSPECTION_SETTINGS_STORAGE_KEY, 'real-settings');
    vi.stubGlobal('localStorage', storage);
    vi.stubGlobal('window', {
      localStorage: storage,
      location: { hash: '#/demo/codex-inspection', pathname: '/' },
    });

    const restore = installDemoInspectionState();
    const lastRun = JSON.parse(
      storage.getItem(CODEX_INSPECTION_LAST_RUN_STORAGE_KEY) ?? '{}'
    ) as Record<string, unknown>;
    const settings = JSON.parse(
      storage.getItem(CODEX_INSPECTION_SETTINGS_STORAGE_KEY) ?? '{}'
    ) as Record<string, unknown>;

    expect(lastRun).toMatchObject({
      version: 1,
      actionFilter: 'all',
      logsCollapsed: false,
      result: { results: expect.arrayContaining([expect.objectContaining({ provider: 'xai' })]) },
    });
    expect(lastRun.logs).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ detail: expect.objectContaining({ triggerKey: 'manual' }) }),
      ])
    );
    expect(settings).toMatchObject({
      targetTypes: ['codex', 'xai'],
      xaiInferenceEnabled: true,
      autoActionMode: 'disable',
      autoRecoverEnabled: true,
    });

    restore();

    expect(storage.getItem(CODEX_INSPECTION_LAST_RUN_STORAGE_KEY)).toBe('real-last-run');
    expect(storage.getItem(CODEX_INSPECTION_SETTINGS_STORAGE_KEY)).toBe('real-settings');
  });
});
