import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  buildProviderRows,
  ClaudeEditDrawer,
  CodexEditDrawer,
  filterAndSortProviderRows,
  GeminiEditDrawer,
  OpenAIEditDrawer,
  PROVIDER_KIND_LABELS,
  ProviderDetailDrawer,
  ProviderHealthCheckDrawer,
  ProviderTable,
  ProviderToolbar,
  VertexEditDrawer,
  useProviderRecentRequests,
  type ProviderHealthCheckApplyAction,
  type ProviderKind,
  type ProviderKindFilter,
  type ProviderRow,
  type ProviderSortDirection,
  type ProviderSortOption,
} from '@/components/providers';
import {
  withDisableAllModelsRule,
  withoutDisableAllModelsRule,
} from '@/components/providers/utils';
import { usePageTransitionLayer } from '@/components/common/PageTransitionLayer';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { EmptyState } from '@/components/ui/EmptyState';
import { Select } from '@/components/ui/Select';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { providersApi } from '@/services/api';
import { useAuthStore, useConfigStore, useNotificationStore, useThemeStore } from '@/stores';
import type { CloakConfig, GeminiKeyConfig, OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import styles from './AiProvidersPage.module.scss';

const PROVIDER_TABLE_DEFAULT_PAGE_SIZE = 10;
const PROVIDER_TABLE_PAGE_SIZE_OPTIONS = [10, 20, 50] as const;

const DEFAULT_CLOAK_CONFIG: CloakConfig = {
  mode: 'auto',
  strictMode: false,
  sensitiveWords: [],
};

export function AiProvidersPage() {
  const { t } = useTranslation();
  const { showNotification, showConfirmation } = useNotificationStore();
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);

  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const updateConfigValue = useConfigStore((state) => state.updateConfigValue);
  const clearCache = useConfigStore((state) => state.clearCache);
  const isCacheValid = useConfigStore((state) => state.isCacheValid);

  const hasMounted = useRef(false);
  const [loading, setLoading] = useState(() => !isCacheValid());
  const [error, setError] = useState('');

  const [geminiKeys, setGeminiKeys] = useState<GeminiKeyConfig[]>(
    () => config?.geminiApiKeys || []
  );
  const [codexConfigs, setCodexConfigs] = useState<ProviderKeyConfig[]>(
    () => config?.codexApiKeys || []
  );
  const [claudeConfigs, setClaudeConfigs] = useState<ProviderKeyConfig[]>(
    () => config?.claudeApiKeys || []
  );
  const [vertexConfigs, setVertexConfigs] = useState<ProviderKeyConfig[]>(
    () => config?.vertexApiKeys || []
  );
  const [openaiProviders, setOpenaiProviders] = useState<OpenAIProviderConfig[]>(
    () => config?.openaiCompatibility || []
  );

  const [configSwitchingKey, setConfigSwitchingKey] = useState<string | null>(null);

  // 表格筛选 / 排序 / 详情状态
  const [kindFilter, setKindFilter] = useState<ProviderKindFilter>('all');
  const [searchText, setSearchText] = useState('');
  const [selectedModels, setSelectedModels] = useState<Set<string>>(new Set());
  const [sortOption, setSortOption] = useState<ProviderSortOption>('priority');
  const [sortDirection, setSortDirection] = useState<ProviderSortDirection>('desc');
  const [detailRowKey, setDetailRowKey] = useState<string | null>(null);
  const [healthCheckOpen, setHealthCheckOpen] = useState(false);
  const [editDrawerKind, setEditDrawerKind] = useState<ProviderKind | null>(null);
  const [editDrawerIndex, setEditDrawerIndex] = useState<number | null>(null);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(PROVIDER_TABLE_DEFAULT_PAGE_SIZE);

  const disableControls = connectionStatus !== 'connected';
  const isSwitching = Boolean(configSwitchingKey);
  const actionsDisabled = disableControls || loading || isSwitching;

  const pageTransitionLayer = usePageTransitionLayer();
  const isCurrentLayer = pageTransitionLayer ? pageTransitionLayer.status === 'current' : true;

  const { usageByProvider, loadRecentRequests, refreshRecentRequests } = useProviderRecentRequests({
    enabled: isCurrentLayer,
  });

  const getErrorMessage = (err: unknown) => {
    if (err instanceof Error) return err.message;
    if (typeof err === 'string') return err;
    return '';
  };

  const loadConfigs = useCallback(async () => {
    const hasValidCache = isCacheValid();
    if (!hasValidCache) {
      setLoading(true);
    }
    setError('');
    try {
      const [configResult, vertexResult, openaiResult] = await Promise.allSettled([
        fetchConfig(),
        providersApi.getVertexConfigs(),
        providersApi.getOpenAIProviders(),
      ]);

      if (configResult.status !== 'fulfilled') {
        throw configResult.reason;
      }

      const data = configResult.value;
      setGeminiKeys(data?.geminiApiKeys || []);
      setCodexConfigs(data?.codexApiKeys || []);
      setClaudeConfigs(data?.claudeApiKeys || []);
      setVertexConfigs(data?.vertexApiKeys || []);
      setOpenaiProviders(data?.openaiCompatibility || []);

      if (vertexResult.status === 'fulfilled') {
        setVertexConfigs(vertexResult.value || []);
        updateConfigValue('vertex-api-key', vertexResult.value || []);
        clearCache('vertex-api-key');
      }

      if (openaiResult.status === 'fulfilled') {
        setOpenaiProviders(openaiResult.value || []);
        updateConfigValue('openai-compatibility', openaiResult.value || []);
        clearCache('openai-compatibility');
      }
    } catch (err: unknown) {
      const message = getErrorMessage(err) || t('notification.refresh_failed');
      setError(message);
    } finally {
      setLoading(false);
    }
  }, [clearCache, fetchConfig, isCacheValid, t, updateConfigValue]);

  useEffect(() => {
    if (hasMounted.current) return;
    hasMounted.current = true;
    loadConfigs();
  }, [loadConfigs]);

  useEffect(() => {
    if (!isCurrentLayer) return;
    void loadRecentRequests().catch(() => {});
  }, [isCurrentLayer, loadRecentRequests]);

  useEffect(() => {
    if (config?.geminiApiKeys) setGeminiKeys(config.geminiApiKeys);
    if (config?.codexApiKeys) setCodexConfigs(config.codexApiKeys);
    if (config?.claudeApiKeys) setClaudeConfigs(config.claudeApiKeys);
    if (config?.vertexApiKeys) setVertexConfigs(config.vertexApiKeys);
    if (config?.openaiCompatibility) setOpenaiProviders(config.openaiCompatibility);
  }, [
    config?.geminiApiKeys,
    config?.codexApiKeys,
    config?.claudeApiKeys,
    config?.vertexApiKeys,
    config?.openaiCompatibility,
  ]);

  const handleRecentRequestsRefresh = useCallback(async () => {
    await refreshRecentRequests();
  }, [refreshRecentRequests]);

  useHeaderRefresh(handleRecentRequestsRefresh, isCurrentLayer);

  const openEditorDrawer = useCallback((kind: ProviderKind, editIndex: number | null) => {
    setDetailRowKey(null);
    setEditDrawerKind(kind);
    setEditDrawerIndex(editIndex);
  }, []);

  const closeEditorDrawer = useCallback(() => {
    setEditDrawerKind(null);
    setEditDrawerIndex(null);
  }, []);

  const handleDrawerSaved = useCallback(() => {
    void loadConfigs();
  }, [loadConfigs]);

  // 统一行集合与派生数据
  const rows = useMemo(
    () =>
      buildProviderRows({
        gemini: geminiKeys,
        codex: codexConfigs,
        claude: claudeConfigs,
        vertex: vertexConfigs,
        openai: openaiProviders,
        usageByProvider,
      }),
    [claudeConfigs, codexConfigs, geminiKeys, openaiProviders, usageByProvider, vertexConfigs]
  );

  const allModelNames = useMemo(() => {
    const names = new Set<string>();
    rows.forEach((row) => {
      row.modelNames.forEach((name) => names.add(name));
    });
    return Array.from(names).sort();
  }, [rows]);

  useEffect(() => {
    // 配置变更后清理已不存在的模型筛选项，避免筛选结果一直为空。
    setSelectedModels((prev) => {
      if (prev.size === 0) return prev;

      const availableModels = new Set(allModelNames);
      const next = new Set(Array.from(prev).filter((name) => availableModels.has(name)));
      return next.size === prev.size ? prev : next;
    });
  }, [allModelNames]);

  const visibleRows = useMemo(
    () =>
      filterAndSortProviderRows(rows, {
        kind: kindFilter,
        searchText,
        selectedModels,
        sortOption,
        sortDirection,
      }),
    [kindFilter, rows, searchText, selectedModels, sortDirection, sortOption]
  );

  const totalPages = Math.max(1, Math.ceil(visibleRows.length / pageSize));
  const currentPage = Math.min(page, totalPages);
  const pageStart = (currentPage - 1) * pageSize;
  const pagedRows = visibleRows.slice(pageStart, pageStart + pageSize);
  const pageStartItem = visibleRows.length === 0 ? 0 : pageStart + 1;
  const pageEndItem = Math.min(visibleRows.length, pageStart + pageSize);

  useEffect(() => {
    setPage(1);
  }, [kindFilter, searchText, selectedModels, sortDirection, sortOption]);

  useEffect(() => {
    if (page === currentPage) return;
    setPage(currentPage);
  }, [currentPage, page]);

  const kindCounts = useMemo(() => {
    const counts: Record<ProviderKindFilter, number> = {
      all: rows.length,
      gemini: 0,
      codex: 0,
      claude: 0,
      vertex: 0,
      openai: 0,
    };
    rows.forEach((row) => {
      counts[row.kind] += 1;
    });
    return counts;
  }, [rows]);

  const detailRow = useMemo(
    () => (detailRowKey ? (rows.find((row) => row.key === detailRowKey) ?? null) : null),
    [detailRowKey, rows]
  );

  const filtersActive =
    kindFilter !== 'all' || searchText.trim() !== '' || selectedModels.size > 0;

  const clearFilters = () => {
    setKindFilter('all');
    setSearchText('');
    setSelectedModels(new Set());
  };

  const applyProviderEnabledActions = async (
    actions: Map<string, ProviderHealthCheckApplyAction>
  ) => {
    if (actions.size === 0) return;

    const rowByKey = new Map(rows.map((row) => [row.key, row]));
    const previous = {
      gemini: geminiKeys,
      codex: codexConfigs,
      claude: claudeConfigs,
      vertex: vertexConfigs,
      openai: openaiProviders,
    };
    let nextGemini = geminiKeys;
    let nextCodex = codexConfigs;
    let nextClaude = claudeConfigs;
    let nextVertex = vertexConfigs;
    let nextOpenai = openaiProviders;
    const changed = {
      gemini: false,
      codex: false,
      claude: false,
      vertex: false,
      openai: false,
    };

    actions.forEach((action, providerKey) => {
      const row = rowByKey.get(providerKey);
      if (!row) return;
      const enabled = action === 'enable';
      if (row.enabled === enabled) return;

      if (row.kind === 'gemini') {
        const current = nextGemini[row.originalIndex];
        if (!current) return;
        const excludedModels = enabled
          ? withoutDisableAllModelsRule(current.excludedModels)
          : withDisableAllModelsRule(current.excludedModels);
        nextGemini = nextGemini.map((item, index) =>
          index === row.originalIndex ? { ...item, excludedModels } : item
        );
        changed.gemini = true;
      } else if (row.kind === 'codex') {
        const current = nextCodex[row.originalIndex];
        if (!current) return;
        const excludedModels = enabled
          ? withoutDisableAllModelsRule(current.excludedModels)
          : withDisableAllModelsRule(current.excludedModels);
        nextCodex = nextCodex.map((item, index) =>
          index === row.originalIndex ? { ...item, excludedModels } : item
        );
        changed.codex = true;
      } else if (row.kind === 'claude') {
        const current = nextClaude[row.originalIndex];
        if (!current) return;
        const excludedModels = enabled
          ? withoutDisableAllModelsRule(current.excludedModels)
          : withDisableAllModelsRule(current.excludedModels);
        nextClaude = nextClaude.map((item, index) =>
          index === row.originalIndex ? { ...item, excludedModels } : item
        );
        changed.claude = true;
      } else if (row.kind === 'vertex') {
        const current = nextVertex[row.originalIndex];
        if (!current) return;
        const excludedModels = enabled
          ? withoutDisableAllModelsRule(current.excludedModels)
          : withDisableAllModelsRule(current.excludedModels);
        nextVertex = nextVertex.map((item, index) =>
          index === row.originalIndex ? { ...item, excludedModels } : item
        );
        changed.vertex = true;
      } else {
        const current = nextOpenai[row.originalIndex];
        if (!current) return;
        nextOpenai = nextOpenai.map((item, index) =>
          index === row.originalIndex ? { ...item, disabled: !enabled } : item
        );
        changed.openai = true;
      }
    });

    if (!Object.values(changed).some(Boolean)) {
      showNotification(t('ai_providers.health_check_no_changes'), 'success');
      return;
    }

    setConfigSwitchingKey('health-check');

    const applyLocalState = (
      gemini: GeminiKeyConfig[],
      codex: ProviderKeyConfig[],
      claude: ProviderKeyConfig[],
      vertex: ProviderKeyConfig[],
      openai: OpenAIProviderConfig[]
    ) => {
      if (changed.gemini) {
        setGeminiKeys(gemini);
        updateConfigValue('gemini-api-key', gemini);
        clearCache('gemini-api-key');
      }
      if (changed.codex) {
        setCodexConfigs(codex);
        updateConfigValue('codex-api-key', codex);
        clearCache('codex-api-key');
      }
      if (changed.claude) {
        setClaudeConfigs(claude);
        updateConfigValue('claude-api-key', claude);
        clearCache('claude-api-key');
      }
      if (changed.vertex) {
        setVertexConfigs(vertex);
        updateConfigValue('vertex-api-key', vertex);
        clearCache('vertex-api-key');
      }
      if (changed.openai) {
        setOpenaiProviders(openai);
        updateConfigValue('openai-compatibility', openai);
        clearCache('openai-compatibility');
      }
    };

    applyLocalState(nextGemini, nextCodex, nextClaude, nextVertex, nextOpenai);

    try {
      await Promise.all([
        changed.gemini ? providersApi.saveGeminiKeys(nextGemini) : Promise.resolve(),
        changed.codex ? providersApi.saveCodexConfigs(nextCodex) : Promise.resolve(),
        changed.claude ? providersApi.saveClaudeConfigs(nextClaude) : Promise.resolve(),
        changed.vertex ? providersApi.saveVertexConfigs(nextVertex) : Promise.resolve(),
        changed.openai ? providersApi.saveOpenAIProviders(nextOpenai) : Promise.resolve(),
      ]);
      showNotification(t('ai_providers.health_check_apply_success'), 'success');
    } catch (err: unknown) {
      const message = getErrorMessage(err);
      applyLocalState(
        previous.gemini,
        previous.codex,
        previous.claude,
        previous.vertex,
        previous.openai
      );
      showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
      throw err;
    } finally {
      setConfigSwitchingKey(null);
    }
  };

  const setHealthCheckProviderEnabled = async (providerKey: string, enabled: boolean) => {
    await applyProviderEnabledActions(
      new Map([[providerKey, enabled ? 'enable' : 'disable']])
    );
  };

  // 启停（gemini/codex/claude/vertex 走 excludedModels 规则）
  const setConfigEnabled = async (
    provider: Exclude<ProviderKind, 'openai'>,
    index: number,
    enabled: boolean
  ) => {
    if (provider === 'gemini') {
      const current = geminiKeys[index];
      if (!current) return;

      const switchingKey = `${provider}:${current.apiKey}`;
      setConfigSwitchingKey(switchingKey);

      const previousList = geminiKeys;
      const nextExcluded = enabled
        ? withoutDisableAllModelsRule(current.excludedModels)
        : withDisableAllModelsRule(current.excludedModels);
      const nextItem: GeminiKeyConfig = { ...current, excludedModels: nextExcluded };
      const nextList = previousList.map((item, idx) => (idx === index ? nextItem : item));

      setGeminiKeys(nextList);
      updateConfigValue('gemini-api-key', nextList);
      clearCache('gemini-api-key');

      try {
        await providersApi.saveGeminiKeys(nextList);
        showNotification(
          enabled ? t('notification.config_enabled') : t('notification.config_disabled'),
          'success'
        );
      } catch (err: unknown) {
        const message = getErrorMessage(err);
        setGeminiKeys(previousList);
        updateConfigValue('gemini-api-key', previousList);
        clearCache('gemini-api-key');
        showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
      } finally {
        setConfigSwitchingKey(null);
      }
      return;
    }

    const source =
      provider === 'codex'
        ? codexConfigs
        : provider === 'claude'
          ? claudeConfigs
          : vertexConfigs;
    const current = source[index];
    if (!current) return;

    const switchingKey = `${provider}:${current.apiKey}`;
    setConfigSwitchingKey(switchingKey);

    const previousList = source;
    const nextExcluded = enabled
      ? withoutDisableAllModelsRule(current.excludedModels)
      : withDisableAllModelsRule(current.excludedModels);
    const nextItem: ProviderKeyConfig = { ...current, excludedModels: nextExcluded };
    const nextList = previousList.map((item, idx) => (idx === index ? nextItem : item));

    if (provider === 'codex') {
      setCodexConfigs(nextList);
      updateConfigValue('codex-api-key', nextList);
      clearCache('codex-api-key');
    } else if (provider === 'claude') {
      setClaudeConfigs(nextList);
      updateConfigValue('claude-api-key', nextList);
      clearCache('claude-api-key');
    } else {
      setVertexConfigs(nextList);
      updateConfigValue('vertex-api-key', nextList);
      clearCache('vertex-api-key');
    }

    try {
      if (provider === 'codex') {
        await providersApi.saveCodexConfigs(nextList);
      } else if (provider === 'claude') {
        await providersApi.saveClaudeConfigs(nextList);
      } else {
        await providersApi.saveVertexConfigs(nextList);
      }
      showNotification(
        enabled ? t('notification.config_enabled') : t('notification.config_disabled'),
        'success'
      );
    } catch (err: unknown) {
      const message = getErrorMessage(err);
      if (provider === 'codex') {
        setCodexConfigs(previousList);
        updateConfigValue('codex-api-key', previousList);
        clearCache('codex-api-key');
      } else if (provider === 'claude') {
        setClaudeConfigs(previousList);
        updateConfigValue('claude-api-key', previousList);
        clearCache('claude-api-key');
      } else {
        setVertexConfigs(previousList);
        updateConfigValue('vertex-api-key', previousList);
        clearCache('vertex-api-key');
      }
      showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
    } finally {
      setConfigSwitchingKey(null);
    }
  };

  const setOpenAIProviderEnabled = async (index: number, enabled: boolean) => {
    const current = openaiProviders[index];
    if (!current) return;

    const switchingKey = `openai:${current.name}:${index}`;
    setConfigSwitchingKey(switchingKey);

    const previousList = openaiProviders;
    const nextItem: OpenAIProviderConfig = { ...current, disabled: !enabled };
    const nextList = previousList.map((item, idx) => (idx === index ? nextItem : item));

    setOpenaiProviders(nextList);
    updateConfigValue('openai-compatibility', nextList);
    clearCache('openai-compatibility');

    try {
      await providersApi.updateOpenAIProviderDisabled(index, !enabled);
      showNotification(
        enabled ? t('notification.config_enabled') : t('notification.config_disabled'),
        'success'
      );
    } catch (err: unknown) {
      const message = getErrorMessage(err);
      setOpenaiProviders(previousList);
      updateConfigValue('openai-compatibility', previousList);
      clearCache('openai-compatibility');
      showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
    } finally {
      setConfigSwitchingKey(null);
    }
  };

  const setProviderWebsocketsEnabled = async (
    provider: 'codex' | 'claude',
    index: number,
    enabled: boolean
  ) => {
    const source = provider === 'codex' ? codexConfigs : claudeConfigs;
    const current = source[index];
    if (!current) return;

    const switchingKey = `${provider}:${current.apiKey}:websockets`;
    setConfigSwitchingKey(switchingKey);

    const previousList = source;
    const nextItem: ProviderKeyConfig = { ...current, websockets: enabled };
    const nextList = previousList.map((item, idx) => (idx === index ? nextItem : item));

    if (provider === 'codex') {
      setCodexConfigs(nextList);
      updateConfigValue('codex-api-key', nextList);
      clearCache('codex-api-key');
    } else {
      setClaudeConfigs(nextList);
      updateConfigValue('claude-api-key', nextList);
      clearCache('claude-api-key');
    }

    try {
      if (provider === 'codex') {
        await providersApi.saveCodexConfigs(nextList);
        showNotification(t('notification.codex_config_updated'), 'success');
      } else {
        await providersApi.saveClaudeConfigs(nextList);
        showNotification(t('notification.claude_config_updated'), 'success');
      }
    } catch (err: unknown) {
      const message = getErrorMessage(err);
      if (provider === 'codex') {
        setCodexConfigs(previousList);
        updateConfigValue('codex-api-key', previousList);
        clearCache('codex-api-key');
      } else {
        setClaudeConfigs(previousList);
        updateConfigValue('claude-api-key', previousList);
        clearCache('claude-api-key');
      }
      showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
    } finally {
      setConfigSwitchingKey(null);
    }
  };

  const setProviderCloakEnabled = async (
    provider: 'codex' | 'claude',
    index: number,
    enabled: boolean
  ) => {
    const source = provider === 'codex' ? codexConfigs : claudeConfigs;
    const current = source[index];
    if (!current) return;

    const switchingKey = `${provider}:${current.apiKey}:cloak`;
    setConfigSwitchingKey(switchingKey);

    const previousList = source;
    const nextItem: ProviderKeyConfig = enabled
      ? { ...current, cloak: current.cloak ?? { ...DEFAULT_CLOAK_CONFIG, sensitiveWords: [] } }
      : { ...current };
    if (!enabled) {
      delete nextItem.cloak;
    }
    const nextList = previousList.map((item, idx) => (idx === index ? nextItem : item));

    if (provider === 'codex') {
      setCodexConfigs(nextList);
      updateConfigValue('codex-api-key', nextList);
      clearCache('codex-api-key');
    } else {
      setClaudeConfigs(nextList);
      updateConfigValue('claude-api-key', nextList);
      clearCache('claude-api-key');
    }

    try {
      if (provider === 'codex') {
        await providersApi.saveCodexConfigs(nextList);
        showNotification(t('notification.codex_config_updated'), 'success');
      } else {
        await providersApi.saveClaudeConfigs(nextList);
        showNotification(t('notification.claude_config_updated'), 'success');
      }
    } catch (err: unknown) {
      const message = getErrorMessage(err);
      if (provider === 'codex') {
        setCodexConfigs(previousList);
        updateConfigValue('codex-api-key', previousList);
        clearCache('codex-api-key');
      } else {
        setClaudeConfigs(previousList);
        updateConfigValue('claude-api-key', previousList);
        clearCache('claude-api-key');
      }
      showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
    } finally {
      setConfigSwitchingKey(null);
    }
  };

  const setProviderDisableCoolingEnabled = async (
    provider: 'gemini' | 'codex' | 'claude' | 'openai',
    index: number,
    enabled: boolean
  ) => {
    if (provider === 'gemini') {
      const current = geminiKeys[index];
      if (!current) return;

      const switchingKey = `${provider}:${current.apiKey}:disable-cooling`;
      setConfigSwitchingKey(switchingKey);

      const previousList = geminiKeys;
      const nextItem: GeminiKeyConfig = { ...current, disableCooling: enabled };
      const nextList = previousList.map((item, idx) => (idx === index ? nextItem : item));

      setGeminiKeys(nextList);
      updateConfigValue('gemini-api-key', nextList);
      clearCache('gemini-api-key');

      try {
        await providersApi.saveGeminiKeys(nextList);
        showNotification(t('notification.gemini_key_updated'), 'success');
      } catch (err: unknown) {
        const message = getErrorMessage(err);
        setGeminiKeys(previousList);
        updateConfigValue('gemini-api-key', previousList);
        clearCache('gemini-api-key');
        showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
      } finally {
        setConfigSwitchingKey(null);
      }
      return;
    }

    if (provider === 'openai') {
      const current = openaiProviders[index];
      if (!current) return;

      const switchingKey = `${provider}:${current.name}:${index}:disable-cooling`;
      setConfigSwitchingKey(switchingKey);

      const previousList = openaiProviders;
      const nextItem: OpenAIProviderConfig = { ...current, disableCooling: enabled };
      const nextList = previousList.map((item, idx) => (idx === index ? nextItem : item));

      setOpenaiProviders(nextList);
      updateConfigValue('openai-compatibility', nextList);
      clearCache('openai-compatibility');

      try {
        await providersApi.saveOpenAIProviders(nextList);
        showNotification(t('notification.openai_provider_updated'), 'success');
      } catch (err: unknown) {
        const message = getErrorMessage(err);
        setOpenaiProviders(previousList);
        updateConfigValue('openai-compatibility', previousList);
        clearCache('openai-compatibility');
        showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
      } finally {
        setConfigSwitchingKey(null);
      }
      return;
    }

    const source = provider === 'codex' ? codexConfigs : claudeConfigs;
    const current = source[index];
    if (!current) return;

    const switchingKey = `${provider}:${current.apiKey}:disable-cooling`;
    setConfigSwitchingKey(switchingKey);

    const previousList = source;
    const nextItem: ProviderKeyConfig = { ...current, disableCooling: enabled };
    const nextList = previousList.map((item, idx) => (idx === index ? nextItem : item));

    if (provider === 'codex') {
      setCodexConfigs(nextList);
      updateConfigValue('codex-api-key', nextList);
      clearCache('codex-api-key');
    } else {
      setClaudeConfigs(nextList);
      updateConfigValue('claude-api-key', nextList);
      clearCache('claude-api-key');
    }

    try {
      if (provider === 'codex') {
        await providersApi.saveCodexConfigs(nextList);
        showNotification(t('notification.codex_config_updated'), 'success');
      } else {
        await providersApi.saveClaudeConfigs(nextList);
        showNotification(t('notification.claude_config_updated'), 'success');
      }
    } catch (err: unknown) {
      const message = getErrorMessage(err);
      if (provider === 'codex') {
        setCodexConfigs(previousList);
        updateConfigValue('codex-api-key', previousList);
        clearCache('codex-api-key');
      } else {
        setClaudeConfigs(previousList);
        updateConfigValue('claude-api-key', previousList);
        clearCache('claude-api-key');
      }
      showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
    } finally {
      setConfigSwitchingKey(null);
    }
  };

  const setProviderPriority = async (row: ProviderRow, priority: number) => {
    const nextPriority = Math.trunc(priority);
    const switchingKey = `${row.key}:priority`;

    // 复用页面级切换锁，避免外层快捷优先级与抽屉保存、开关切换并发写入。
    if (row.kind === 'gemini') {
      const current = geminiKeys[row.originalIndex];
      if (!current || current.priority === nextPriority) return;

      setConfigSwitchingKey(switchingKey);
      const previousList = geminiKeys;
      const nextList = previousList.map((item, idx) =>
        idx === row.originalIndex ? { ...item, priority: nextPriority } : item
      );

      setGeminiKeys(nextList);
      updateConfigValue('gemini-api-key', nextList);
      clearCache('gemini-api-key');

      try {
        await providersApi.saveGeminiKeys(nextList);
        showNotification(t('notification.gemini_key_updated'), 'success');
      } catch (err: unknown) {
        const message = getErrorMessage(err);
        setGeminiKeys(previousList);
        updateConfigValue('gemini-api-key', previousList);
        clearCache('gemini-api-key');
        showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
      } finally {
        setConfigSwitchingKey(null);
      }
      return;
    }

    if (row.kind === 'openai') {
      const current = openaiProviders[row.originalIndex];
      if (!current || current.priority === nextPriority) return;

      setConfigSwitchingKey(switchingKey);
      const previousList = openaiProviders;
      const nextList = previousList.map((item, idx) =>
        idx === row.originalIndex ? { ...item, priority: nextPriority } : item
      );

      setOpenaiProviders(nextList);
      updateConfigValue('openai-compatibility', nextList);
      clearCache('openai-compatibility');

      try {
        await providersApi.saveOpenAIProviders(nextList);
        showNotification(t('notification.openai_provider_updated'), 'success');
      } catch (err: unknown) {
        const message = getErrorMessage(err);
        setOpenaiProviders(previousList);
        updateConfigValue('openai-compatibility', previousList);
        clearCache('openai-compatibility');
        showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
      } finally {
        setConfigSwitchingKey(null);
      }
      return;
    }

    const source =
      row.kind === 'codex'
        ? codexConfigs
        : row.kind === 'claude'
          ? claudeConfigs
          : vertexConfigs;
    const current = source[row.originalIndex];
    if (!current || current.priority === nextPriority) return;

    setConfigSwitchingKey(switchingKey);
    const previousList = source;
    const nextList = previousList.map((item, idx) =>
      idx === row.originalIndex ? { ...item, priority: nextPriority } : item
    );

    if (row.kind === 'codex') {
      setCodexConfigs(nextList);
      updateConfigValue('codex-api-key', nextList);
      clearCache('codex-api-key');
    } else if (row.kind === 'claude') {
      setClaudeConfigs(nextList);
      updateConfigValue('claude-api-key', nextList);
      clearCache('claude-api-key');
    } else {
      setVertexConfigs(nextList);
      updateConfigValue('vertex-api-key', nextList);
      clearCache('vertex-api-key');
    }

    try {
      if (row.kind === 'codex') {
        await providersApi.saveCodexConfigs(nextList);
        showNotification(t('notification.codex_config_updated'), 'success');
      } else if (row.kind === 'claude') {
        await providersApi.saveClaudeConfigs(nextList);
        showNotification(t('notification.claude_config_updated'), 'success');
      } else {
        await providersApi.saveVertexConfigs(nextList);
        showNotification(t('notification.vertex_config_updated'), 'success');
      }
    } catch (err: unknown) {
      const message = getErrorMessage(err);
      if (row.kind === 'codex') {
        setCodexConfigs(previousList);
        updateConfigValue('codex-api-key', previousList);
        clearCache('codex-api-key');
      } else if (row.kind === 'claude') {
        setClaudeConfigs(previousList);
        updateConfigValue('claude-api-key', previousList);
        clearCache('claude-api-key');
      } else {
        setVertexConfigs(previousList);
        updateConfigValue('vertex-api-key', previousList);
        clearCache('vertex-api-key');
      }
      showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
    } finally {
      setConfigSwitchingKey(null);
    }
  };

  // 删除（按 provider 分派，沿用既有 API 契约）
  const deleteGemini = (index: number) => {
    const entry = geminiKeys[index];
    if (!entry) return;
    showConfirmation({
      title: t('ai_providers.gemini_delete_title', { defaultValue: 'Delete Gemini Key' }),
      message: t('ai_providers.gemini_delete_confirm'),
      variant: 'danger',
      confirmText: t('common.confirm'),
      onConfirm: async () => {
        try {
          await providersApi.deleteGeminiKey(entry.apiKey, entry.baseUrl);
          const next = geminiKeys.filter((_, idx) => idx !== index);
          setGeminiKeys(next);
          updateConfigValue('gemini-api-key', next);
          clearCache('gemini-api-key');
          showNotification(t('notification.gemini_key_deleted'), 'success');
        } catch (err: unknown) {
          const message = getErrorMessage(err);
          showNotification(`${t('notification.delete_failed')}: ${message}`, 'error');
        }
      },
    });
  };

  const deleteProviderEntry = (type: 'codex' | 'claude', index: number) => {
    const source = type === 'codex' ? codexConfigs : claudeConfigs;
    const entry = source[index];
    if (!entry) return;
    showConfirmation({
      title: t(`ai_providers.${type}_delete_title`, {
        defaultValue: `Delete ${type === 'codex' ? 'Codex' : 'Claude'} Config`,
      }),
      message: t(`ai_providers.${type}_delete_confirm`),
      variant: 'danger',
      confirmText: t('common.confirm'),
      onConfirm: async () => {
        try {
          if (type === 'codex') {
            await providersApi.deleteCodexConfig(entry.apiKey, entry.baseUrl);
            const next = codexConfigs.filter((_, idx) => idx !== index);
            setCodexConfigs(next);
            updateConfigValue('codex-api-key', next);
            clearCache('codex-api-key');
            showNotification(t('notification.codex_config_deleted'), 'success');
          } else {
            await providersApi.deleteClaudeConfig(entry.apiKey, entry.baseUrl);
            const next = claudeConfigs.filter((_, idx) => idx !== index);
            setClaudeConfigs(next);
            updateConfigValue('claude-api-key', next);
            clearCache('claude-api-key');
            showNotification(t('notification.claude_config_deleted'), 'success');
          }
        } catch (err: unknown) {
          const message = getErrorMessage(err);
          showNotification(`${t('notification.delete_failed')}: ${message}`, 'error');
        }
      },
    });
  };

  const deleteVertex = (index: number) => {
    const entry = vertexConfigs[index];
    if (!entry) return;
    showConfirmation({
      title: t('ai_providers.vertex_delete_title', { defaultValue: 'Delete Vertex Config' }),
      message: t('ai_providers.vertex_delete_confirm'),
      variant: 'danger',
      confirmText: t('common.confirm'),
      onConfirm: async () => {
        try {
          await providersApi.deleteVertexConfig(entry.apiKey, entry.baseUrl);
          const next = vertexConfigs.filter((_, idx) => idx !== index);
          setVertexConfigs(next);
          updateConfigValue('vertex-api-key', next);
          clearCache('vertex-api-key');
          showNotification(t('notification.vertex_config_deleted'), 'success');
        } catch (err: unknown) {
          const message = getErrorMessage(err);
          showNotification(`${t('notification.delete_failed')}: ${message}`, 'error');
        }
      },
    });
  };

  const deleteOpenai = (index: number) => {
    const entry = openaiProviders[index];
    if (!entry) return;
    showConfirmation({
      title: t('ai_providers.openai_delete_title', { defaultValue: 'Delete OpenAI Provider' }),
      message: t('ai_providers.openai_delete_confirm'),
      variant: 'danger',
      confirmText: t('common.confirm'),
      onConfirm: async () => {
        try {
          await providersApi.deleteOpenAIProvider(entry.name);
          const next = openaiProviders.filter((_, idx) => idx !== index);
          setOpenaiProviders(next);
          updateConfigValue('openai-compatibility', next);
          clearCache('openai-compatibility');
          showNotification(t('notification.openai_provider_deleted'), 'success');
        } catch (err: unknown) {
          const message = getErrorMessage(err);
          showNotification(`${t('notification.delete_failed')}: ${message}`, 'error');
        }
      },
    });
  };

  // 行级回调分派
  const handleRowToggle = (row: ProviderRow, enabled: boolean) => {
    if (row.kind === 'openai') {
      void setOpenAIProviderEnabled(row.originalIndex, enabled);
    } else {
      void setConfigEnabled(row.kind, row.originalIndex, enabled);
    }
  };

  const handleRowWebsocketsToggle = (row: ProviderRow, enabled: boolean) => {
    if (row.kind !== 'codex' && row.kind !== 'claude') return;
    void setProviderWebsocketsEnabled(row.kind, row.originalIndex, enabled);
  };

  const handleRowCloakToggle = (row: ProviderRow, enabled: boolean) => {
    if (row.kind !== 'codex' && row.kind !== 'claude') return;
    void setProviderCloakEnabled(row.kind, row.originalIndex, enabled);
  };

  const handleRowDisableCoolingToggle = (row: ProviderRow, enabled: boolean) => {
    if (
      row.kind !== 'gemini' &&
      row.kind !== 'codex' &&
      row.kind !== 'claude' &&
      row.kind !== 'openai'
    ) {
      return;
    }
    void setProviderDisableCoolingEnabled(row.kind, row.originalIndex, enabled);
  };

  const handleRowPriorityChange = (row: ProviderRow, priority: number) => {
    void setProviderPriority(row, priority);
  };

  const handleRowEdit = (row: ProviderRow) => {
    setDetailRowKey(null);
    openEditorDrawer(row.kind, row.originalIndex);
  };

  const handleRowDelete = (row: ProviderRow) => {
    setDetailRowKey(null);
    if (row.kind === 'gemini') {
      deleteGemini(row.originalIndex);
    } else if (row.kind === 'codex' || row.kind === 'claude') {
      deleteProviderEntry(row.kind, row.originalIndex);
    } else if (row.kind === 'vertex') {
      deleteVertex(row.originalIndex);
    } else {
      deleteOpenai(row.originalIndex);
    }
  };

  const handleAdd = (kind: ProviderKind) => {
    openEditorDrawer(kind, null);
  };

  const handlePageSizeChange = (value: string) => {
    const nextSize = Number.parseInt(value, 10);
    if (!Number.isFinite(nextSize) || nextSize <= 0) return;
    setPageSize(nextSize);
    setPage(1);
  };

  const emptyState =
    rows.length > 0 && kindFilter !== 'all' && kindCounts[kindFilter] === 0 ? (
      // 当前类型尚无配置：直接给“添加该类型配置”入口，避免“清除筛选”死胡同
      <EmptyState
        title={t('ai_providers.kind_empty_title', { name: PROVIDER_KIND_LABELS[kindFilter] })}
        action={
          <Button
            size="sm"
            onClick={() => handleAdd(kindFilter)}
            disabled={actionsDisabled}
          >
            {t('ai_providers.add_kind_button', { name: PROVIDER_KIND_LABELS[kindFilter] })}
          </Button>
        }
      />
    ) : rows.length > 0 && filtersActive ? (
      <EmptyState
        title={t('ai_providers.table_filtered_empty_title')}
        description={t('ai_providers.table_filtered_empty_desc')}
        action={
          <Button variant="secondary" size="sm" onClick={clearFilters} disabled={actionsDisabled}>
            {t('ai_providers.clear_filters')}
          </Button>
        }
      />
    ) : (
      <EmptyState
        title={t('ai_providers.table_empty_title')}
        description={t('ai_providers.table_empty_desc')}
      />
    );

  return (
    <div className={styles.container}>
      <div className={styles.content}>
        {error && <div className="error-box">{error}</div>}

        <div>
          <ProviderToolbar
            kind={kindFilter}
            kindCounts={kindCounts}
            onKindChange={setKindFilter}
            searchText={searchText}
            onSearchTextChange={setSearchText}
            allModelNames={allModelNames}
            selectedModels={selectedModels}
            onSelectedModelsChange={setSelectedModels}
            sortOption={sortOption}
            onSortOptionChange={setSortOption}
            sortDirection={sortDirection}
            onSortDirectionChange={setSortDirection}
            disabled={actionsDisabled}
            resolvedTheme={resolvedTheme}
            onAdd={handleAdd}
            onHealthCheck={() => setHealthCheckOpen(true)}
            healthCheckDisabled={visibleRows.length === 0}
          />

          <Card>
            <ProviderTable
              rows={pagedRows}
              loading={loading}
              actionsDisabled={actionsDisabled}
              toggleDisabled={actionsDisabled}
              resolvedTheme={resolvedTheme}
              emptyState={emptyState}
              onShowDetail={(row) => setDetailRowKey(row.key)}
              onEdit={handleRowEdit}
              onDelete={handleRowDelete}
              onToggle={handleRowToggle}
              onPriorityChange={handleRowPriorityChange}
            />
            {visibleRows.length > 0 &&
              (visibleRows.length > PROVIDER_TABLE_DEFAULT_PAGE_SIZE ||
                pageSize !== PROVIDER_TABLE_DEFAULT_PAGE_SIZE) && (
              <div className={styles.paginationBar}>
                <div className={styles.paginationInfo}>
                  {t('monitoring.pagination_info', {
                    current: currentPage,
                    total: totalPages,
                    start: pageStartItem,
                    end: pageEndItem,
                    count: visibleRows.length,
                  })}
                </div>
                <div className={styles.paginationControls}>
                  <div className={styles.pageSizeField}>
                    <span>{t('monitoring.page_size_label')}</span>
                    <Select
                      value={String(pageSize)}
                      options={PROVIDER_TABLE_PAGE_SIZE_OPTIONS.map((size) => ({
                        value: String(size),
                        label: t('monitoring.page_size_option', { count: size }),
                      }))}
                      onChange={handlePageSizeChange}
                      disabled={loading}
                      fullWidth={false}
                      ariaLabel={t('monitoring.page_size_label')}
                      className={styles.pageSizeSelect}
                      triggerClassName={styles.pageSizeSelectTrigger}
                    />
                  </div>
                  <Button
                    variant="secondary"
                    size="xs"
                    onClick={() => setPage(Math.max(1, currentPage - 1))}
                    disabled={loading || currentPage <= 1}
                  >
                    {t('monitoring.pagination_prev')}
                  </Button>
                  <Button
                    variant="secondary"
                    size="xs"
                    onClick={() => setPage(Math.min(totalPages, currentPage + 1))}
                    disabled={loading || currentPage >= totalPages}
                  >
                    {t('monitoring.pagination_next')}
                  </Button>
                </div>
              </div>
            )}
          </Card>
        </div>
      </div>

      <ProviderDetailDrawer
        row={detailRow}
        open={detailRowKey !== null}
        usageByProvider={usageByProvider}
        resolvedTheme={resolvedTheme}
        actionsDisabled={actionsDisabled}
        toggleDisabled={actionsDisabled}
        onClose={() => setDetailRowKey(null)}
        onEdit={handleRowEdit}
        onDelete={handleRowDelete}
        onToggle={handleRowToggle}
        onToggleWebsockets={handleRowWebsocketsToggle}
        onToggleCloak={handleRowCloakToggle}
        onToggleDisableCooling={handleRowDisableCoolingToggle}
      />
      <ProviderHealthCheckDrawer
        open={healthCheckOpen}
        rows={visibleRows}
        actionsDisabled={actionsDisabled}
        onClose={() => setHealthCheckOpen(false)}
        onApplyResultActions={applyProviderEnabledActions}
        onSetProviderEnabled={setHealthCheckProviderEnabled}
      />
      <GeminiEditDrawer
        open={editDrawerKind === 'gemini'}
        editIndex={editDrawerIndex}
        disabled={actionsDisabled}
        onClose={closeEditorDrawer}
        onSaved={handleDrawerSaved}
      />
      <CodexEditDrawer
        open={editDrawerKind === 'codex'}
        editIndex={editDrawerIndex}
        disabled={actionsDisabled}
        onClose={closeEditorDrawer}
        onSaved={handleDrawerSaved}
      />
      <VertexEditDrawer
        open={editDrawerKind === 'vertex'}
        editIndex={editDrawerIndex}
        disabled={actionsDisabled}
        onClose={closeEditorDrawer}
        onSaved={handleDrawerSaved}
      />
      <ClaudeEditDrawer
        open={editDrawerKind === 'claude'}
        editIndex={editDrawerIndex}
        disabled={actionsDisabled}
        onClose={closeEditorDrawer}
        onSaved={handleDrawerSaved}
      />
      <OpenAIEditDrawer
        open={editDrawerKind === 'openai'}
        editIndex={editDrawerIndex}
        disabled={actionsDisabled}
        onClose={closeEditorDrawer}
        onSaved={handleDrawerSaved}
      />
    </div>
  );
}
