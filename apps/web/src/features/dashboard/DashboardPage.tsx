import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import {
  IconBot,
  IconFileText,
  IconKey,
  IconRefreshCw,
  IconSatellite,
  IconSettings,
} from '@/components/ui/icons';
import { useAuthStore, useConfigStore, useModelsStore } from '@/stores';
import { apiKeysApi, providersApi, authFilesApi } from '@/services/api';
import { logsApi, type ErrorLogFile } from '@/services/api/logs';
import {
  usageServiceApi,
  type ApiKeyAlias,
  type UsageServiceStatus,
} from '@/services/api/usageService';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { loadMonitoringMetaPayload } from '@/features/monitoring/services/monitoringMetaService';
import { buildMonitoringAuthMetaMap } from '@/features/monitoring/model/authMeta';
import { buildAuthFileMapFromMeta } from '@/features/monitoring/model/sourceDisplay';
import type { MonitoringChannelMeta } from '@/features/monitoring/model/types';
import { buildSourceInfoMap } from '@/utils/sourceResolver';
import type { AuthFileItem } from '@/types/authFile';
import { VersionCard } from './components/VersionCard';
import { UsageMetricsCard } from './components/UsageMetricsCard';
import { CollectorStatusCard } from './components/CollectorStatusCard';
import { HealthAlertsCard } from './components/HealthAlertsCard';
import { TrafficOverviewCard } from './components/TrafficOverviewCard';
import { useDashboardUsageSummary } from './hooks/useDashboardUsageSummary';
import { getDashboardModelCountDisplay } from './modelCountDisplay';
import { resolveProviderCount } from './providerStats';
import styles from './DashboardPage.module.scss';

interface QuickStat {
  label: string;
  value: number | string;
  icon: React.ReactNode;
  path: string;
  loading?: boolean;
  sublabel?: string;
}

interface ProviderStats {
  gemini: number | null;
  codex: number | null;
  xai: number | null;
  claude: number | null;
  openai: number | null;
}

interface DashboardDisplayMeta {
  authFiles: AuthFileItem[];
  channels: MonitoringChannelMeta[];
  apiKeyAliases: ApiKeyAlias[];
}

const HEALTH_REFRESH_INTERVAL_MS = 60_000;

export function DashboardPage() {
  const { t, i18n } = useTranslation();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const serverVersion = useAuthStore((state) => state.serverVersion);
  const serverBuildDate = useAuthStore((state) => state.serverBuildDate);
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey);
  const config = useConfigStore((state) => state.config);
  const usageSummary = useDashboardUsageSummary();
  const refreshUsageSummary = usageSummary.refresh;

  const models = useModelsStore((state) => state.models);
  const modelsLoading = useModelsStore((state) => state.loading);
  const modelsError = useModelsStore((state) => state.error);
  const fetchModelsFromStore = useModelsStore((state) => state.fetchModels);

  const [stats, setStats] = useState<{
    apiKeys: number | null;
    authFiles: number | null;
  }>({
    apiKeys: null,
    authFiles: null,
  });

  const [providerStats, setProviderStats] = useState<ProviderStats>({
    gemini: null,
    codex: null,
    xai: null,
    claude: null,
    openai: null,
  });

  const [loading, setLoading] = useState(true);

  const [currentTime, setCurrentTime] = useState(() => new Date());
  const [cardRefreshSignal, setCardRefreshSignal] = useState(0);
  const [collectorStatus, setCollectorStatus] = useState<UsageServiceStatus | null>(null);
  const [collectorLoading, setCollectorLoading] = useState(false);
  const [collectorError, setCollectorError] = useState('');
  const [errorLogs, setErrorLogs] = useState<ErrorLogFile[]>([]);
  const [errorLogsLoading, setErrorLogsLoading] = useState(false);
  const [managerCpaBase, setManagerCpaBase] = useState('');
  const [displayMeta, setDisplayMeta] = useState<DashboardDisplayMeta>({
    authFiles: [],
    channels: [],
    apiKeyAliases: [],
  });

  const apiKeysCache = useRef<string[]>([]);

  useEffect(() => {
    apiKeysCache.current = [];
  }, [apiBase, config?.apiKeys]);

  // Update time every 60 seconds
  useEffect(() => {
    const id = setInterval(() => {
      setCurrentTime(new Date());
    }, 60_000);
    return () => clearInterval(id);
  }, []);

  const normalizeApiKeyList = (input: unknown): string[] => {
    if (!Array.isArray(input)) return [];
    const seen = new Set<string>();
    const keys: string[] = [];

    input.forEach((item) => {
      const record =
        item !== null && typeof item === 'object' && !Array.isArray(item)
          ? (item as Record<string, unknown>)
          : null;
      const value =
        typeof item === 'string'
          ? item
          : record
            ? (record['api-key'] ?? record['apiKey'] ?? record.key ?? record.Key)
            : '';
      const trimmed = String(value ?? '').trim();
      if (!trimmed || seen.has(trimmed)) return;
      seen.add(trimmed);
      keys.push(trimmed);
    });

    return keys;
  };

  const resolveApiKeysForModels = useCallback(async () => {
    if (apiKeysCache.current.length) {
      return apiKeysCache.current;
    }

    const configKeys = normalizeApiKeyList(config?.apiKeys);
    if (configKeys.length) {
      apiKeysCache.current = configKeys;
      return configKeys;
    }

    try {
      const list = await apiKeysApi.list();
      const normalized = normalizeApiKeyList(list);
      if (normalized.length) {
        apiKeysCache.current = normalized;
      }
      return normalized;
    } catch {
      return [];
    }
  }, [config?.apiKeys]);

  const fetchModels = useCallback(async () => {
    if (connectionStatus !== 'connected' || !apiBase) {
      return;
    }

    try {
      const apiKeys = await resolveApiKeysForModels();
      const primaryKey = apiKeys[0];
      await fetchModelsFromStore(apiBase, primaryKey);
    } catch {
      // Ignore model fetch errors on dashboard
    }
  }, [connectionStatus, apiBase, resolveApiKeysForModels, fetchModelsFromStore]);

  const refreshStats = useCallback(async () => {
    if (connectionStatus === 'connected') {
      setLoading(true);
      try {
        const [keysRes, filesRes, geminiRes, codexRes, xaiRes, claudeRes, openaiRes] =
          await Promise.allSettled([
            apiKeysApi.list(),
            authFilesApi.list(),
            providersApi.getGeminiKeys(),
            providersApi.getCodexConfigs(),
            providersApi.getXAIConfigs(),
            providersApi.getClaudeConfigs(),
            providersApi.getOpenAIProviders(),
          ]);

        setStats({
          apiKeys: keysRes.status === 'fulfilled' ? keysRes.value.length : null,
          authFiles: filesRes.status === 'fulfilled' ? filesRes.value.files.length : null,
        });

        setProviderStats({
          gemini: resolveProviderCount(geminiRes),
          codex: resolveProviderCount(codexRes),
          xai: resolveProviderCount(xaiRes, { unsupportedAsZero: true }),
          claude: resolveProviderCount(claudeRes),
          openai: resolveProviderCount(openaiRes),
        });
      } finally {
        setLoading(false);
      }
    } else {
      setLoading(false);
    }
  }, [connectionStatus]);

  const usageEnabled = usageSummary.enabled;
  const usageServiceBase = usageSummary.serviceBase;
  const authMetaMap = useMemo(
    () => buildMonitoringAuthMetaMap(displayMeta.authFiles),
    [displayMeta.authFiles]
  );
  const authFileMap = useMemo(() => buildAuthFileMapFromMeta(authMetaMap), [authMetaMap]);
  const sourceInfoMap = useMemo(
    () =>
      buildSourceInfoMap({
        geminiApiKeys: config?.geminiApiKeys || [],
        claudeApiKeys: config?.claudeApiKeys || [],
        codexApiKeys: config?.codexApiKeys || [],
        xaiApiKeys: config?.xaiApiKeys || [],
        vertexApiKeys: config?.vertexApiKeys || [],
        openaiCompatibility: config?.openaiCompatibility || [],
      }),
    [config]
  );
  const channelByAuthIndex = useMemo(() => {
    const map = new Map<string, MonitoringChannelMeta>();
    displayMeta.channels.forEach((channel) => {
      channel.authIndices.forEach((authIndex) => {
        map.set(authIndex, channel);
      });
    });
    return map;
  }, [displayMeta.channels]);
  const apiKeyAliasMap = useMemo(() => {
    const map = new Map<string, string>();
    displayMeta.apiKeyAliases.forEach((item) => {
      const hash = item.apiKeyHash?.trim().toLowerCase();
      const alias = item.alias?.trim();
      if (hash && alias) {
        map.set(hash, alias);
      }
    });
    return map;
  }, [displayMeta.apiKeyAliases]);

  const refreshHealth = useCallback(async () => {
    if (!usageEnabled || !usageServiceBase) {
      setCollectorStatus(null);
      setCollectorError('');
      setCollectorLoading(false);
      setErrorLogs([]);
      setErrorLogsLoading(false);
      setManagerCpaBase('');
      setDisplayMeta({ authFiles: [], channels: [], apiKeyAliases: [] });
      return;
    }

    setCollectorLoading(true);
    setErrorLogsLoading(true);

    const [collectorResult, logsResult, managerConfigResult, metaResult, aliasesResult] =
      await Promise.allSettled([
        usageServiceApi.getStatus(usageServiceBase, managementKey),
        logsApi.fetchErrorLogs(),
        usageServiceApi.getManagerConfig(usageServiceBase, managementKey),
        loadMonitoringMetaPayload(config),
        usageServiceApi.getApiKeyAliases(usageServiceBase, managementKey),
      ]);

    if (collectorResult.status === 'fulfilled') {
      setCollectorStatus(collectorResult.value);
      setCollectorError('');
    } else {
      setCollectorStatus(null);
      const reason = collectorResult.reason;
      setCollectorError(reason instanceof Error ? reason.message : String(reason));
    }
    setCollectorLoading(false);

    if (logsResult.status === 'fulfilled') {
      setErrorLogs(Array.isArray(logsResult.value.files) ? logsResult.value.files : []);
    } else {
      setErrorLogs([]);
    }
    setErrorLogsLoading(false);

    setManagerCpaBase(
      managerConfigResult.status === 'fulfilled'
        ? managerConfigResult.value.config.cpaConnection?.cpaBaseUrl || apiBase || ''
        : apiBase || ''
    );

    setDisplayMeta((current) => ({
      authFiles: metaResult.status === 'fulfilled' ? metaResult.value.authFiles : current.authFiles,
      channels: metaResult.status === 'fulfilled' ? metaResult.value.channels : current.channels,
      apiKeyAliases:
        aliasesResult.status === 'fulfilled' && Array.isArray(aliasesResult.value.items)
          ? aliasesResult.value.items
          : current.apiKeyAliases,
    }));
  }, [apiBase, config, managementKey, usageEnabled, usageServiceBase]);

  const refreshDashboard = useCallback(async () => {
    setCurrentTime(new Date());
    setCardRefreshSignal((value) => value + 1);
    await Promise.all([refreshStats(), fetchModels(), refreshUsageSummary(), refreshHealth()]);
  }, [fetchModels, refreshHealth, refreshStats, refreshUsageSummary]);

  useEffect(() => {
    void Promise.all([refreshStats(), fetchModels()]);
  }, [fetchModels, refreshStats]);

  useEffect(() => {
    void refreshHealth();
  }, [refreshHealth]);

  useEffect(() => {
    if (!usageEnabled) return;
    const timer = window.setInterval(() => {
      void refreshHealth();
    }, HEALTH_REFRESH_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, [refreshHealth, usageEnabled]);

  useHeaderRefresh(refreshDashboard);

  // Calculate total provider keys only when all provider stats are available.
  const providerStatsReady =
    providerStats.gemini !== null &&
    providerStats.codex !== null &&
    providerStats.xai !== null &&
    providerStats.claude !== null &&
    providerStats.openai !== null;
  const hasProviderStats =
    providerStats.gemini !== null ||
    providerStats.codex !== null ||
    providerStats.xai !== null ||
    providerStats.claude !== null ||
    providerStats.openai !== null;
  const totalProviderKeys = providerStatsReady
    ? (providerStats.gemini ?? 0) +
      (providerStats.codex ?? 0) +
      (providerStats.xai ?? 0) +
      (providerStats.claude ?? 0) +
      (providerStats.openai ?? 0)
    : 0;

  const quickStats: QuickStat[] = [
    {
      label: t('dashboard.management_keys'),
      value: stats.apiKeys ?? '-',
      icon: <IconKey size={24} />,
      path: '/config',
      loading: loading && stats.apiKeys === null,
      sublabel: t('nav.config_management'),
    },
    {
      label: t('nav.ai_providers'),
      value: loading ? '-' : providerStatsReady ? totalProviderKeys : '-',
      icon: <IconBot size={24} />,
      path: '/ai-providers',
      loading: loading,
      sublabel: hasProviderStats
        ? t('dashboard.provider_keys_detail', {
            gemini: providerStats.gemini ?? '-',
            codex: providerStats.codex ?? '-',
            xai: providerStats.xai ?? '-',
            claude: providerStats.claude ?? '-',
            openai: providerStats.openai ?? '-',
          })
        : undefined,
    },
    {
      label: t('nav.auth_files'),
      value: stats.authFiles ?? '-',
      icon: <IconFileText size={24} />,
      path: '/auth-files',
      loading: loading && stats.authFiles === null,
      sublabel: t('dashboard.oauth_credentials'),
    },
    {
      label: t('dashboard.available_models'),
      value: getDashboardModelCountDisplay(models.length, modelsLoading, modelsError),
      icon: <IconSatellite size={24} />,
      path: '/system',
      loading: modelsLoading,
      sublabel: t('dashboard.available_models_desc'),
    },
  ];

  const routingStrategyRaw = config?.routingStrategy?.trim() || '';
  const routingStrategyDisplay = !routingStrategyRaw
    ? '-'
    : routingStrategyRaw === 'round-robin'
      ? t('basic_settings.routing_strategy_round_robin')
      : routingStrategyRaw === 'fill-first'
        ? t('basic_settings.routing_strategy_fill_first')
        : routingStrategyRaw;
  const routingStrategyBadgeClass = !routingStrategyRaw
    ? styles.configBadgeUnknown
    : routingStrategyRaw === 'round-robin'
      ? styles.configBadgeRoundRobin
      : routingStrategyRaw === 'fill-first'
        ? styles.configBadgeFillFirst
        : styles.configBadgeUnknown;

  const formattedDate = currentTime.toLocaleDateString(i18n.language, {
    weekday: 'long',
  });

  const formattedDateTime = currentTime.toLocaleString(i18n.language, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  });

  return (
    <>
      {/* 1. Header Section */}
      <header className={styles.header}>
        <div className={styles.headerLeft}>
          <div className={styles.connectionStatus}>
            <span
              className={`${styles.statusDot} ${
                connectionStatus === 'connected'
                  ? styles.connected
                  : connectionStatus === 'connecting'
                    ? styles.connecting
                    : styles.disconnected
              }`}
            />
            <div className={styles.statusInfo}>
              <span className={styles.statusLabel}>{t('common.connection_status')}</span>
              <span className={styles.statusValue}>
                {t(
                  connectionStatus === 'connected'
                    ? 'common.connected'
                    : connectionStatus === 'connecting'
                      ? 'common.connecting'
                      : 'common.disconnected'
                )}
              </span>
            </div>
          </div>
          <div className={styles.apiBaseBlock}>
            <span className={styles.apiLabel}>{t('dashboard.api_base')}</span>
            <span className={styles.apiValue}>{apiBase || 'http://localhost:3000'}</span>
          </div>
        </div>

        <div className={styles.headerRight}>
          <div className={styles.timeDisplay}>
            <span className={styles.time}>{formattedDateTime}</span>
            <span className={styles.date}>{formattedDate}</span>
          </div>
          <div className={styles.headerActions}>
            <button
              className={styles.actionBtn}
              onClick={refreshDashboard}
              title={t('common.refresh')}
            >
              <IconRefreshCw size={16} />
              <span>{t('common.refresh')}</span>
            </button>
            <Link to="/config" className={styles.actionBtn} title={t('nav.system_config')}>
              <IconSettings size={16} />
            </Link>
          </div>
        </div>
      </header>

      {/* 2. Top Overview Row (Version & Health) */}
      <section className={styles.overviewRow}>
        <VersionCard
          appVersion={__APP_VERSION__ || t('dashboard.version_unknown')}
          apiVersion={serverVersion || t('dashboard.version_unknown')}
          cpaBase={managerCpaBase || apiBase || ''}
          serverBuildDate={serverBuildDate || undefined}
          connectionStatus={connectionStatus}
          refreshSignal={cardRefreshSignal}
          usageEnabled={usageSummary.enabled}
          usageLoading={usageSummary.loading}
          usageError={usageSummary.error}
          collectorStatus={collectorStatus}
          collectorLoading={collectorLoading}
          collectorError={collectorError}
          errorLogCount={errorLogs.length}
          errorLogsLoading={errorLogsLoading}
        />
      </section>

      {/* 3. Today's Overview (Metrics Cards) */}
      {usageSummary.enabled && (
        <section className={styles.metricsRow}>
          <h2 className={styles.sectionTitle}>{t('dashboard.today_overview_usage_service')}</h2>
          <UsageMetricsCard
            summary={usageSummary.summary}
            topModels={usageSummary.topModels}
            modelCostRank={usageSummary.modelCostRank}
            loading={usageSummary.loading}
            error={usageSummary.error}
            lastRefreshedAt={usageSummary.lastRefreshedAt}
            mode="metrics-only"
          />
        </section>
      )}

      {/* 4. Charts Row (Traffic, Activity, Tokens) */}
      {usageSummary.enabled && (
        <section className={styles.chartsRow}>
          <TrafficOverviewCard
            timeline={usageSummary.trafficTimeline}
            trafficNowMs={usageSummary.summary?.window.now_ms}
            todayRequestHealthTimeline={usageSummary.todayRequestHealthTimeline}
            tokenMix={usageSummary.tokenMix}
            loading={usageSummary.loading}
          />
        </section>
      )}

      {/* 5. Data & Status Row (Rankings, Health, Failures) */}
      {usageSummary.enabled && (
        <section className={styles.dataRow}>
          <UsageMetricsCard
            summary={usageSummary.summary}
            topModels={usageSummary.topModels}
            modelCostRank={usageSummary.modelCostRank}
            loading={usageSummary.loading}
            error={usageSummary.error}
            lastRefreshedAt={usageSummary.lastRefreshedAt}
            mode="rank-only"
          />
          <HealthAlertsCard
            loading={usageSummary.loading}
            recentFailures={usageSummary.recentFailures}
            channelHealth={usageSummary.channelHealth}
            authMetaMap={authMetaMap}
            authFileMap={authFileMap}
            sourceInfoMap={sourceInfoMap}
            channelByAuthIndex={channelByAuthIndex}
            apiKeyAliasMap={apiKeyAliasMap}
          />
          <CollectorStatusCard
            enabled={usageSummary.enabled}
            serviceBase={usageSummary.serviceBase}
            managementKey={managementKey}
            refreshSignal={cardRefreshSignal}
            status={collectorStatus}
            loading={collectorLoading}
            error={collectorError}
          />
        </section>
      )}

      {/* 6. Quick Stats + Config Summary */}
      <section className={styles.bottomSummaryRow}>
        <div className={styles.quickStatsPanel}>
          <div className={styles.bentoGrid}>
            {quickStats.map((stat, index) => (
              <Link
                key={stat.path}
                to={stat.path}
                className={styles.bentoCard}
                style={{ animationDelay: `${index * 80}ms` }}
              >
                <div className={styles.bentoIcon}>{stat.icon}</div>
                <div className={styles.bentoContent}>
                  <span className={styles.bentoLabel}>{stat.label}</span>
                  <span className={styles.bentoValue}>{stat.loading ? '...' : stat.value}</span>
                  {stat.sublabel && !stat.loading && (
                    <span className={styles.bentoSublabel}>{stat.sublabel}</span>
                  )}
                </div>
                <div className={styles.bentoArrow}>{t('dashboard.manage')} →</div>
              </Link>
            ))}
          </div>
        </div>

        {config && (
          <div className={styles.configCard}>
            <div className={styles.configHeader}>
              <h3>{t('dashboard.current_config_summary')}</h3>
            </div>
            <div className={styles.configGrid}>
              <div className={styles.configItem}>
                <span className={styles.configLabel}>Debug</span>
                <span className={`${styles.configValue} ${config.debug ? styles.on : styles.off}`}>
                  {config.debug ? t('common.enabled') : t('common.disabled')}
                </span>
              </div>
              <div className={styles.configItem}>
                <span className={styles.configLabel}>
                  {t('basic_settings.logging_to_file_enable')}
                </span>
                <span
                  className={`${styles.configValue} ${config.loggingToFile ? styles.on : styles.off}`}
                >
                  {config.loggingToFile ? t('common.enabled') : t('common.disabled')}
                </span>
              </div>
              <div className={styles.configItem}>
                <span className={styles.configLabel}>{t('basic_settings.retry_count_label')}</span>
                <span className={styles.configValue}>{config.requestRetry ?? 0}</span>
              </div>
              <div className={styles.configItem}>
                <span className={styles.configLabel}>{t('basic_settings.ws_auth_enable')}</span>
                <span className={`${styles.configValue} ${config.wsAuth ? styles.on : styles.off}`}>
                  {config.wsAuth ? t('common.enabled') : t('common.disabled')}
                </span>
              </div>
              <div className={styles.configItem}>
                <span className={styles.configLabel}>{t('dashboard.routing_strategy')}</span>
                <span className={`${styles.configBadge} ${routingStrategyBadgeClass}`}>
                  {routingStrategyDisplay}
                </span>
              </div>
              {config.proxyUrl && (
                <div className={`${styles.configItem} ${styles.fullWidth}`}>
                  <span className={styles.configLabel}>Proxy URL</span>
                  <span className={styles.configValueMono}>{config.proxyUrl}</span>
                </div>
              )}
            </div>
            <Link to="/config" className={styles.configLink}>
              {t('dashboard.view_full_config')} →
            </Link>
          </div>
        )}
      </section>
    </>
  );
}
