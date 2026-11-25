import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  buildProviderRows,
  filterAndSortProviderRows,
  PROVIDER_KIND_LABELS,
  ProviderCardGrid,
  ProviderHealthCheckDrawer,
  ProviderTable,
  ProviderToolbar,
  ProviderWorkspaceDrawer,
  useProviderRecentRequests,
  type ProviderDrawerTab,
  type ProviderHealthCheckApplyAction,
  type ProviderHealthFilter,
  type ProviderKind,
  type ProviderKindFilter,
  type ProviderLayoutMode,
  type ProviderRow,
  type ProviderSavedIdentityHint,
  type ProviderSortDirection,
  type ProviderSortOption,
  type ProviderStatusFilter,
} from '@/components/providers';
import { useProviderAccountHistory } from '@/components/providers/hooks/useProviderAccountHistory';
import type { ProviderWorkspaceDrawerHandle } from '@/components/providers/ProviderWorkspaceDrawer';
import { rowMatchesSavedAnchor } from '@/components/providers/ProviderWorkspaceDrawer/useProviderDrawerEditor';
import {
  withDisableAllModelsRule,
  withoutDisableAllModelsRule,
} from '@/components/providers/utils';
import { FloatingActionDock } from '@/components/common/FloatingActionDock';
import { LocalizedPaginationFooter } from '@/components/common/LocalizedPaginationFooter';
import { usePageTransitionLayer } from '@/components/common/PageTransitionLayer';
import { WorkspaceListPanel } from '@/components/common/WorkspaceListPanel';
import { Button } from '@/components/ui/Button';
import { DropdownMenu } from '@/components/ui/DropdownMenu';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { IconMoreVertical, IconTrash2 } from '@/components/ui/icons';
import {
  useClientPagination,
  useHeaderRefresh,
  useListSelection,
  useMediaQuery,
  usePersistedViewMode,
} from '@/hooks';
import { providersApi } from '@/services/api';
import { getUsageServiceErrorCode } from '@/services/api/usageService';
import { useAuthStore, useConfigStore, useNotificationStore, useThemeStore } from '@/stores';
import type { GeminiKeyConfig, OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import {
  PROVIDER_WORKSPACE_PAGE_SIZE_OPTIONS,
  readAiProvidersWorkspaceUiState,
  writeAiProvidersWorkspaceUiState,
} from './model/aiProvidersWorkspaceUiState';
import { createConfigMutationLock } from './model/configMutationLock';
import { buildProviderDeleteSecondConfirmation } from './model/deleteConfirmation';
import styles from './AiProvidersPage.module.scss';

interface ProviderListsSnapshot {
  gemini: GeminiKeyConfig[];
  interactions: GeminiKeyConfig[];
  codex: ProviderKeyConfig[];
  xai: ProviderKeyConfig[];
  claude: ProviderKeyConfig[];
  vertex: ProviderKeyConfig[];
  openai: OpenAIProviderConfig[];
}

const hasAnyV8SourceTag = (lists: ProviderListsSnapshot): boolean =>
  [
    ...lists.gemini,
    ...lists.interactions,
    ...lists.codex,
    ...lists.xai,
    ...lists.claude,
    ...lists.vertex,
    ...lists.openai,
  ].some((entry) => Boolean(entry?.v8Source));

const cloneProviderLists = (lists: ProviderListsSnapshot): ProviderListsSnapshot => ({
  gemini: [...lists.gemini],
  interactions: [...lists.interactions],
  codex: [...lists.codex],
  xai: [...lists.xai],
  claude: [...lists.claude],
  vertex: [...lists.vertex],
  openai: [...lists.openai],
});

const createChangedMap = (onlyKind?: ProviderKind): Record<ProviderKind, boolean> => ({
  gemini: onlyKind === 'gemini',
  interactions: onlyKind === 'interactions',
  codex: onlyKind === 'codex',
  xai: onlyKind === 'xai',
  claude: onlyKind === 'claude',
  vertex: onlyKind === 'vertex',
  openai: onlyKind === 'openai',
});

const normalizeIdentityText = (value?: string): string => String(value ?? '').trim();

const readLatestV8ConfigEtag = (): string | undefined => {
  if (
    'getLatestV8ConfigEtag' in providersApi &&
    typeof providersApi.getLatestV8ConfigEtag === 'function'
  ) {
    const value = providersApi.getLatestV8ConfigEtag();
    return typeof value === 'string' && value.trim() ? value.trim() : undefined;
  }
  return undefined;
};

const extractWriteResultEtag = (res: unknown): string | undefined => {
  if (res && typeof res === 'object' && 'etag' in res && typeof res.etag === 'string') {
    return res.etag.trim() || undefined;
  }
  return undefined;
};

const findFirstV8EtagInList = (
  items: ReadonlyArray<{ v8Source?: { etag?: string } }>
): string | undefined => {
  for (const item of items) {
    const etag = typeof item?.v8Source?.etag === 'string' ? item.v8Source.etag.trim() : '';
    if (etag) return etag;
  }
  return undefined;
};

const findFirstV8EtagInLists = (lists: ProviderListsSnapshot): string | undefined =>
  findFirstV8EtagInList(lists.gemini) ??
  findFirstV8EtagInList(lists.interactions) ??
  findFirstV8EtagInList(lists.codex) ??
  findFirstV8EtagInList(lists.xai) ??
  findFirstV8EtagInList(lists.claude) ??
  findFirstV8EtagInList(lists.vertex) ??
  findFirstV8EtagInList(lists.openai);

const findFirstV8EtagInChangedLists = (
  lists: ProviderListsSnapshot,
  changed: Record<ProviderKind, boolean>
): string | undefined => {
  const kinds: ProviderKind[] = [
    'gemini',
    'interactions',
    'codex',
    'xai',
    'claude',
    'vertex',
    'openai',
  ];
  for (const kind of kinds) {
    if (!changed[kind]) continue;
    const etag = findFirstV8EtagInList(lists[kind]);
    if (etag) return etag;
  }
  return undefined;
};

const getErrorStatus = (err: unknown): number | undefined => {
  if (!err || typeof err !== 'object') return undefined;
  if ('status' in err && typeof err.status === 'number') {
    return err.status;
  }
  if (
    'response' in err &&
    err.response &&
    typeof err.response === 'object' &&
    'status' in err.response &&
    typeof err.response.status === 'number'
  ) {
    return err.response.status;
  }
  return undefined;
};

const isPartialOrConflictError = (err: unknown): boolean => {
  if (!err || typeof err !== 'object') return false;
  const partial = 'partialCommitted' in err && err.partialCommitted === true;
  return partial || getErrorStatus(err) === 412;
};

const findMatchingFreshRow = (rows: ProviderRow[], targetRow: ProviderRow): ProviderRow | null => {
  const exact = rows.find((row) => row.key === targetRow.key);
  if (exact) return exact;

  const sameKind = rows.filter((row) => row.kind === targetRow.kind);
  if (sameKind.length === 0) return null;

  const targetAuthIndex = normalizeIdentityText(targetRow.raw.authIndex);
  if (targetAuthIndex) {
    const byAuth = sameKind.find(
      (row) => normalizeIdentityText(row.raw.authIndex) === targetAuthIndex
    );
    if (byAuth) return byAuth;
  }

  if (targetRow.kind === 'openai') {
    const targetName = normalizeIdentityText(targetRow.raw.name);
    return (
      sameKind.find((row) => normalizeIdentityText(row.raw.name) === targetName) ?? null
    );
  }

  const targetApiKey = normalizeIdentityText(targetRow.raw.apiKey);
  const targetBaseUrl = normalizeIdentityText(targetRow.baseUrl);
  const targetName = normalizeIdentityText(targetRow.raw.name);

  return (
    sameKind.find(
      (row) =>
        row.kind !== 'openai' &&
        normalizeIdentityText(row.raw.apiKey) === targetApiKey &&
        normalizeIdentityText(row.baseUrl) === targetBaseUrl &&
        normalizeIdentityText(row.raw.name) === targetName
    ) ??
    sameKind.find(
      (row) =>
        row.kind !== 'openai' &&
        normalizeIdentityText(row.raw.apiKey) === targetApiKey &&
        normalizeIdentityText(row.baseUrl) === targetBaseUrl
    ) ??
    null
  );
};

const findReanchoredDrawerRow = (
  nextRows: ProviderRow[],
  kind: ProviderKind,
  previousRow: ProviderRow | null,
  savedIdentity: ProviderSavedIdentityHint
): ProviderRow | null => {
  const kindRows = nextRows.filter((row) => row.kind === kind);
  if (kindRows.length === 0) return null;

  const targetAuthIndex = normalizeIdentityText(savedIdentity.authIndex);
  const targetName = normalizeIdentityText(savedIdentity.name);
  const targetApiKey = normalizeIdentityText(savedIdentity.apiKey);
  const targetBaseUrl = normalizeIdentityText(savedIdentity.baseUrl);
  const targetProxyUrl = normalizeIdentityText(savedIdentity.proxyUrl);
  const targetPrefix = normalizeIdentityText(savedIdentity.prefix);
  const preferredIndex = savedIdentity.originalIndex ?? previousRow?.originalIndex;

  const pickBest = (candidates: ProviderRow[]): ProviderRow | null => {
    if (candidates.length === 0) return null;
    if (candidates.length === 1) return candidates[0];
    if (preferredIndex !== undefined) {
      const sameIndex = candidates.find((row) => row.originalIndex === preferredIndex);
      if (sameIndex) return sameIndex;
    }
    return candidates[0];
  };

  if (targetAuthIndex) {
    const authMatches = kindRows.filter(
      (row) => normalizeIdentityText(row.raw.authIndex) === targetAuthIndex
    );
    const best = pickBest(authMatches);
    if (best) return best;
  }

  const v8Matches = savedIdentity.v8Source
    ? kindRows.filter((row) => {
        const src = row.raw.v8Source;
        const targetSrc = savedIdentity.v8Source;
        if (!src || !targetSrc) return false;
        if (src.family !== targetSrc.family || src.groupIndex !== targetSrc.groupIndex) {
          return false;
        }
        return kind === 'openai' ? true : (src.keyIndex ?? 0) === (targetSrc.keyIndex ?? 0);
      })
    : [];

  if (v8Matches.length > 0) {
    if (kind === 'openai') {
      const refined = targetName
        ? v8Matches.filter((row) => normalizeIdentityText(row.raw.name) === targetName)
        : v8Matches;
      const best = pickBest(refined.length > 0 ? refined : v8Matches);
      if (best) return best;
    } else {
      const refined = v8Matches.filter((row) => {
        if (row.kind === 'openai') return false;
        if (targetApiKey && normalizeIdentityText(row.raw.apiKey) !== targetApiKey) {
          return false;
        }
        if (targetBaseUrl && normalizeIdentityText(row.baseUrl) !== targetBaseUrl) {
          return false;
        }
        return true;
      });
      if (refined.length > 0) {
        const best = pickBest(refined);
        if (best) return best;
      }
    }
  }

  if (kind === 'openai') {
    const previousOpenAIKeys = new Set(
      previousRow?.kind === 'openai'
        ? (previousRow.raw.apiKeyEntries ?? [])
            .map((entry) => normalizeIdentityText(entry.apiKey))
            .filter(Boolean)
        : []
    );
    if (targetApiKey) {
      previousOpenAIKeys.add(targetApiKey);
    }
    const hasMatchingOpenAIKeyEntry = (row: ProviderRow): boolean => {
      if (row.kind !== 'openai' || previousOpenAIKeys.size === 0) return false;
      return (row.raw.apiKeyEntries ?? []).some((entry) =>
        previousOpenAIKeys.has(normalizeIdentityText(entry.apiKey))
      );
    };

    if (targetName && targetBaseUrl) {
      const nameAndUrlMatches = kindRows.filter(
        (row) =>
          normalizeIdentityText(row.raw.name) === targetName &&
          normalizeIdentityText(row.baseUrl) === targetBaseUrl
      );
      const best = pickBest(nameAndUrlMatches);
      if (best) return best;
    }
    if (savedIdentity.originalIndex !== undefined) {
      const indexRow = kindRows.find((row) => row.originalIndex === savedIdentity.originalIndex);
      if (
        indexRow &&
        ((targetName && normalizeIdentityText(indexRow.raw.name) === targetName) ||
          (targetBaseUrl && normalizeIdentityText(indexRow.baseUrl) === targetBaseUrl) ||
          hasMatchingOpenAIKeyEntry(indexRow))
      ) {
        return indexRow;
      }
    }
    if (targetName && previousOpenAIKeys.size > 0) {
      const nameAndKeyMatches = kindRows.filter(
        (row) =>
          normalizeIdentityText(row.raw.name) === targetName && hasMatchingOpenAIKeyEntry(row)
      );
      const best = pickBest(nameAndKeyMatches);
      if (best) return best;
    }
    if (targetName) {
      const nameMatches = kindRows.filter(
        (row) => normalizeIdentityText(row.raw.name) === targetName
      );
      const best = pickBest(nameMatches);
      if (best) return best;
    }
    if (targetBaseUrl && previousOpenAIKeys.size > 0) {
      const urlAndKeyMatches = kindRows.filter(
        (row) =>
          normalizeIdentityText(row.baseUrl) === targetBaseUrl && hasMatchingOpenAIKeyEntry(row)
      );
      const best = pickBest(urlAndKeyMatches);
      if (best) return best;
    }
    if (targetBaseUrl) {
      const urlMatches = kindRows.filter(
        (row) => normalizeIdentityText(row.baseUrl) === targetBaseUrl
      );
      const best = pickBest(urlMatches);
      if (best) return best;
    }
    if (previousOpenAIKeys.size > 0) {
      const keyMatches = kindRows.filter((row) => hasMatchingOpenAIKeyEntry(row));
      const best = pickBest(keyMatches);
      if (best) return best;
    }
    if (savedIdentity.originalIndex !== undefined) {
      const byIndex = kindRows.find((row) => row.originalIndex === savedIdentity.originalIndex);
      if (byIndex) return byIndex;
    }
    return null;
  }

  if (targetApiKey && targetBaseUrl) {
    const keyAndUrlMatches = kindRows.filter(
      (row) =>
        row.kind !== 'openai' &&
        normalizeIdentityText(row.raw.apiKey) === targetApiKey &&
        normalizeIdentityText(row.baseUrl) === targetBaseUrl
    );
    if (keyAndUrlMatches.length > 0) {
      const nameRefined = targetName
        ? keyAndUrlMatches.filter((row) => normalizeIdentityText(row.raw.name) === targetName)
        : keyAndUrlMatches;
      const best = pickBest(nameRefined.length > 0 ? nameRefined : keyAndUrlMatches);
      if (best) return best;
    }
  }

  if (targetName) {
    const nameMatches = kindRows.filter((row) => {
      if (row.kind === 'openai') return false;
      if (normalizeIdentityText(row.raw.name) !== targetName) return false;
      if (savedIdentity.apiKey !== undefined && normalizeIdentityText(row.raw.apiKey) !== targetApiKey) {
        return false;
      }
      if (savedIdentity.baseUrl !== undefined && normalizeIdentityText(row.baseUrl) !== targetBaseUrl) {
        return false;
      }
      return true;
    });
    const best = pickBest(nameMatches);
    if (best) return best;
  }

  if (targetApiKey) {
    const keyMatches = kindRows.filter(
      (row) =>
        row.kind !== 'openai' && normalizeIdentityText(row.raw.apiKey) === targetApiKey
    );
    const best = pickBest(keyMatches);
    if (best) return best;
  }

  if (targetBaseUrl) {
    const urlMatches = kindRows.filter(
      (row) =>
        row.kind !== 'openai' &&
        normalizeIdentityText(row.baseUrl) === targetBaseUrl &&
        normalizeIdentityText(row.raw.apiKey) === targetApiKey
    );
    const best = pickBest(urlMatches);
    if (best) return best;
  }

  if (targetName) {
    const looseNameMatches = kindRows.filter(
      (row) => row.kind !== 'openai' && normalizeIdentityText(row.raw.name) === targetName
    );
    const best = pickBest(looseNameMatches);
    if (best) return best;
  }

  if (targetProxyUrl || targetPrefix) {
    const fallbackMatches = kindRows.filter(
      (row) =>
        row.kind !== 'openai' &&
        (!targetProxyUrl || normalizeIdentityText(row.raw.proxyUrl) === targetProxyUrl) &&
        (!targetPrefix || normalizeIdentityText(row.prefix) === targetPrefix)
    );
    const best = pickBest(fallbackMatches);
    if (best) return best;
  }

  if (v8Matches.length > 0) {
    const best = pickBest(v8Matches);
    if (best) return best;
  }

  if (savedIdentity.originalIndex !== undefined) {
    const byIndex = kindRows.find((row) => row.originalIndex === savedIdentity.originalIndex);
    if (byIndex) return byIndex;
  }

  return null;
};

const areProviderListsJsonEqual = (left: unknown[], right: unknown[]): boolean => {
  if (left === right) return true;
  if (left.length !== right.length) return false;
  return JSON.stringify(left) === JSON.stringify(right);
};

const matchesDistinguishingRowIdentity = (snapshot: ProviderRow, candidate: ProviderRow): boolean => {
  if (snapshot.kind !== candidate.kind) return false;
  if (snapshot.kind === 'openai' && candidate.kind === 'openai') {
    const prevKeySig = (snapshot.raw.apiKeyEntries ?? [])
      .map((entry) => `${normalizeIdentityText(entry.apiKey)}::${normalizeIdentityText(entry.proxyUrl)}`)
      .join('|');
    const nextKeySig = (candidate.raw.apiKeyEntries ?? [])
      .map((entry) => `${normalizeIdentityText(entry.apiKey)}::${normalizeIdentityText(entry.proxyUrl)}`)
      .join('|');
    return (
      normalizeIdentityText(snapshot.raw.name) === normalizeIdentityText(candidate.raw.name) &&
      normalizeIdentityText(snapshot.baseUrl) === normalizeIdentityText(candidate.baseUrl) &&
      normalizeIdentityText(snapshot.prefix) === normalizeIdentityText(candidate.prefix) &&
      prevKeySig === nextKeySig
    );
  }
  return (
    candidate.kind !== 'openai' &&
    snapshot.kind !== 'openai' &&
    normalizeIdentityText(snapshot.raw.apiKey) === normalizeIdentityText(candidate.raw.apiKey) &&
    normalizeIdentityText(snapshot.baseUrl) === normalizeIdentityText(candidate.baseUrl) &&
    normalizeIdentityText(snapshot.raw.proxyUrl) === normalizeIdentityText(candidate.raw.proxyUrl) &&
    normalizeIdentityText(snapshot.raw.name) === normalizeIdentityText(candidate.raw.name)
  );
};

const findShiftedDrawerRow = (
  nextRows: ProviderRow[],
  snapshot: ProviderRow | null
): ProviderRow | null => {
  if (!snapshot) return null;
  const kindRows = nextRows.filter((row) => row.kind === snapshot.kind);
  if (kindRows.length === 0) return null;

  const targetAuthIndex = normalizeIdentityText(snapshot.raw.authIndex);
  if (targetAuthIndex) {
    const authMatches = kindRows.filter(
      (row) => normalizeIdentityText(row.raw.authIndex) === targetAuthIndex
    );
    return authMatches.length === 1 ? authMatches[0] : null;
  }

  const matches = kindRows.filter((row) => matchesDistinguishingRowIdentity(snapshot, row));
  if (matches.length === 1) return matches[0];
  if (matches.length > 1) {
    const targetModelsSig = snapshot.modelNames
      .map((name) => name.trim())
      .filter(Boolean)
      .sort()
      .join('|');
    const modelMatches = matches.filter(
      (row) =>
        row.modelNames
          .map((name) => name.trim())
          .filter(Boolean)
          .sort()
          .join('|') === targetModelsSig
    );
    if (modelMatches.length === 1) return modelMatches[0];
  }
  return null;
};

const getPerItemDeleteMethod = (kind: ProviderKind) => {
  switch (kind) {
    case 'gemini':
      return 'deleteGeminiKey' in providersApi &&
        typeof providersApi.deleteGeminiKey === 'function'
        ? providersApi.deleteGeminiKey
        : undefined;
    case 'interactions':
      return 'deleteInteractionsKey' in providersApi &&
        typeof providersApi.deleteInteractionsKey === 'function'
        ? providersApi.deleteInteractionsKey
        : undefined;
    case 'codex':
      return 'deleteCodexConfig' in providersApi &&
        typeof providersApi.deleteCodexConfig === 'function'
        ? providersApi.deleteCodexConfig
        : undefined;
    case 'xai':
      return 'deleteXAIConfig' in providersApi &&
        typeof providersApi.deleteXAIConfig === 'function'
        ? providersApi.deleteXAIConfig
        : undefined;
    case 'claude':
      return 'deleteClaudeConfig' in providersApi &&
        typeof providersApi.deleteClaudeConfig === 'function'
        ? providersApi.deleteClaudeConfig
        : undefined;
    case 'vertex':
      return 'deleteVertexConfig' in providersApi &&
        typeof providersApi.deleteVertexConfig === 'function'
        ? providersApi.deleteVertexConfig
        : undefined;
    case 'openai':
      return 'deleteOpenAIProvider' in providersApi &&
        typeof providersApi.deleteOpenAIProvider === 'function'
        ? providersApi.deleteOpenAIProvider
        : undefined;
  }
};

export function AiProvidersPage() {
  const { t } = useTranslation();
  const { showNotification, showConfirmation } = useNotificationStore();
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const isCompactViewport = useMediaQuery('(max-width: 768px)');

  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const updateConfigValue = useConfigStore((state) => state.updateConfigValue);
  const clearCache = useConfigStore((state) => state.clearCache);
  const isCacheValid = useConfigStore((state) => state.isCacheValid);

  const initialUiState = useMemo(() => readAiProvidersWorkspaceUiState(), []);

  const hasMounted = useRef(false);
  const [loading, setLoading] = useState(() => !isCacheValid());
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState('');
  const [v8AliasSupported, setV8AliasSupported] = useState<boolean>(() => {
    if (typeof config?.v8Supported === 'boolean') {
      return config.v8Supported;
    }
    return hasAnyV8SourceTag({
      gemini: config?.geminiApiKeys || [],
      interactions: config?.interactionsApiKeys || [],
      codex: config?.codexApiKeys || [],
      xai: config?.xaiApiKeys || [],
      claude: config?.claudeApiKeys || [],
      vertex: config?.vertexApiKeys || [],
      openai: config?.openaiCompatibility || [],
    });
  });

  const [geminiKeys, setGeminiKeys] = useState<GeminiKeyConfig[]>(
    () => config?.geminiApiKeys || []
  );
  const [interactionsKeys, setInteractionsKeys] = useState<GeminiKeyConfig[]>(
    () => config?.interactionsApiKeys || []
  );
  const [codexConfigs, setCodexConfigs] = useState<ProviderKeyConfig[]>(
    () => config?.codexApiKeys || []
  );
  const [xaiConfigs, setXAIConfigs] = useState<ProviderKeyConfig[]>(() => config?.xaiApiKeys || []);
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
  const configMutationLockRef = useRef(createConfigMutationLock());
  const beginConfigMutation = useCallback((switchingKey: string) => {
    if (!configMutationLockRef.current.tryAcquire()) return false;
    setConfigSwitchingKey(switchingKey);
    return true;
  }, []);
  const finishConfigMutation = useCallback(() => {
    configMutationLockRef.current.release();
    setConfigSwitchingKey(null);
  }, []);

  const runWithMutationLock = useCallback(
    async <T,>(
      switchingKey: string,
      task: () => Promise<T>
    ): Promise<{ executed: true; value: T } | { executed: false }> => {
      if (!beginConfigMutation(switchingKey)) {
        return { executed: false };
      }
      try {
        const value = await task();
        return { executed: true, value };
      } finally {
        finishConfigMutation();
      }
    },
    [beginConfigMutation, finishConfigMutation]
  );

  // 筛选、排序、视图与分页状态
  const [kindFilter, setKindFilter] = useState<ProviderKindFilter>('all');
  const [statusFilter, setStatusFilter] = useState<ProviderStatusFilter>('all');
  const [healthFilter, setHealthFilter] = useState<ProviderHealthFilter>('all');
  const [searchText, setSearchText] = useState('');
  const [selectedModels, setSelectedModels] = useState<Set<string>>(new Set());
  const [storedLayoutMode, setStoredLayoutMode] = useState<ProviderLayoutMode>(
    initialUiState.layoutMode
  );
  const [sortOption, setSortOption] = useState<ProviderSortOption>(initialUiState.sortOption);
  const [sortDirection, setSortDirection] = useState<ProviderSortDirection>(
    initialUiState.sortDirection
  );
  const [storedPageSize, setStoredPageSize] = useState(initialUiState.pageSize);

  // 批量弹窗状态
  const [batchPriorityModalOpen, setBatchPriorityModalOpen] = useState(false);
  const [batchPriorityText, setBatchPriorityText] = useState('');
  // 统一工作区抽屉与健康检查抽屉状态
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [drawerMode, setDrawerMode] = useState<'view-edit' | 'create'>('view-edit');
  const [drawerRowKey, setDrawerRowKey] = useState<string | null>(null);
  const [drawerInitialTab, setDrawerInitialTab] = useState<ProviderDrawerTab>('overview');
  const [drawerCreateKind, setDrawerCreateKind] = useState<ProviderKind>('openai');
  const [healthCheckOpen, setHealthCheckOpen] = useState(false);
  const [healthCheckScope, setHealthCheckScope] = useState<'filtered' | 'selected'>('filtered');
  const drawerControllerRef = useRef<ProviderWorkspaceDrawerHandle | null>(null);
  const [drawerSessionId, setDrawerSessionId] = useState(0);
  const [drawerRowSnapshot, setDrawerRowSnapshot] = useState<ProviderRow | null>(null);
  const [drawerOrphanedRow, setDrawerOrphanedRow] = useState<ProviderRow | null>(null);
  const [drawerReanchoring, setDrawerReanchoring] = useState(false);
  const pendingSavedIdentityRef = useRef<{
    kind: ProviderKind;
    previousKey: string;
    previousRow: ProviderRow | null;
    savedIdentity: ProviderSavedIdentityHint;
  } | null>(null);
  const orphanNotifiedKeyRef = useRef<string | null>(null);

  useEffect(() => {
    writeAiProvidersWorkspaceUiState({
      layoutMode: storedLayoutMode,
      pageSize: storedPageSize,
      sortOption,
      sortDirection,
    });
  }, [sortDirection, sortOption, storedLayoutMode, storedPageSize]);

  const disableControls = connectionStatus !== 'connected';
  const isSwitching = Boolean(configSwitchingKey);
  const actionsDisabled = disableControls || loading || isSwitching;
  const {
    effectiveMode: effectiveLayoutMode,
    setMode: setLayoutMode,
    switcherHidden: viewSwitcherHidden,
  } = usePersistedViewMode<ProviderLayoutMode>({
    defaultMode: 'table',
    validModes: ['table', 'grid'],
    compactFallbackMode: 'grid',
    isCompact: isCompactViewport,
    value: storedLayoutMode,
    onChange: setStoredLayoutMode,
  });
  const pageTransitionLayer = usePageTransitionLayer();
  const isCurrentLayer = pageTransitionLayer ? pageTransitionLayer.status === 'current' : true;

  const { usageByProvider, loadRecentRequests, refreshRecentRequests } = useProviderRecentRequests({
    enabled: isCurrentLayer,
  });
  const { historyByRowKey, refreshAccountHistory } = useProviderAccountHistory({
    enabled: isCurrentLayer,
    gemini: geminiKeys,
    interactions: interactionsKeys,
    codex: codexConfigs,
    xai: xaiConfigs,
    claude: claudeConfigs,
    vertex: vertexConfigs,
    openai: openaiProviders,
  });

  const v8AliasSupportedRef = useRef(v8AliasSupported);
  v8AliasSupportedRef.current = v8AliasSupported;
  const geminiKeysRef = useRef(geminiKeys);
  geminiKeysRef.current = geminiKeys;
  const interactionsKeysRef = useRef(interactionsKeys);
  interactionsKeysRef.current = interactionsKeys;
  const codexConfigsRef = useRef(codexConfigs);
  codexConfigsRef.current = codexConfigs;
  const xaiConfigsRef = useRef(xaiConfigs);
  xaiConfigsRef.current = xaiConfigs;
  const claudeConfigsRef = useRef(claudeConfigs);
  claudeConfigsRef.current = claudeConfigs;
  const vertexConfigsRef = useRef(vertexConfigs);
  vertexConfigsRef.current = vertexConfigs;
  const openaiProvidersRef = useRef(openaiProviders);
  openaiProvidersRef.current = openaiProviders;
  const drawerOpenRef = useRef(drawerOpen);
  drawerOpenRef.current = drawerOpen;
  const drawerModeRef = useRef(drawerMode);
  drawerModeRef.current = drawerMode;
  const drawerRowKeyRef = useRef(drawerRowKey);
  drawerRowKeyRef.current = drawerRowKey;
  const usageByProviderRef = useRef(usageByProvider);
  usageByProviderRef.current = usageByProvider;
  const historyByRowKeyRef = useRef(historyByRowKey);
  historyByRowKeyRef.current = historyByRowKey;
  const supersededConfigRef = useRef<typeof config>(null);

  const getCurrentProviderLists = useCallback(
    (): ProviderListsSnapshot => ({
      gemini: [...geminiKeysRef.current],
      interactions: [...interactionsKeysRef.current],
      codex: [...codexConfigsRef.current],
      xai: [...xaiConfigsRef.current],
      claude: [...claudeConfigsRef.current],
      vertex: [...vertexConfigsRef.current],
      openai: [...openaiProvidersRef.current],
    }),
    []
  );

  const applyProviderListsState = useCallback((lists: ProviderListsSnapshot) => {
    geminiKeysRef.current = lists.gemini;
    interactionsKeysRef.current = lists.interactions;
    codexConfigsRef.current = lists.codex;
    xaiConfigsRef.current = lists.xai;
    claudeConfigsRef.current = lists.claude;
    vertexConfigsRef.current = lists.vertex;
    openaiProvidersRef.current = lists.openai;
    setGeminiKeys(lists.gemini);
    setInteractionsKeys(lists.interactions);
    setCodexConfigs(lists.codex);
    setXAIConfigs(lists.xai);
    setClaudeConfigs(lists.claude);
    setVertexConfigs(lists.vertex);
    setOpenaiProviders(lists.openai);
  }, []);

  const applySingleFamilyState = useCallback(
    (kind: ProviderKind, nextList: ProviderListsSnapshot[ProviderKind]) => {
      switch (kind) {
        case 'gemini': {
          const typed = nextList as GeminiKeyConfig[];
          geminiKeysRef.current = typed;
          setGeminiKeys(typed);
          updateConfigValue('gemini-api-key', typed);
          clearCache('gemini-api-key');
          break;
        }
        case 'interactions': {
          const typed = nextList as GeminiKeyConfig[];
          interactionsKeysRef.current = typed;
          setInteractionsKeys(typed);
          updateConfigValue('interactions-api-key', typed);
          clearCache('interactions-api-key');
          break;
        }
        case 'codex': {
          const typed = nextList as ProviderKeyConfig[];
          codexConfigsRef.current = typed;
          setCodexConfigs(typed);
          updateConfigValue('codex-api-key', typed);
          clearCache('codex-api-key');
          break;
        }
        case 'xai': {
          const typed = nextList as ProviderKeyConfig[];
          xaiConfigsRef.current = typed;
          setXAIConfigs(typed);
          updateConfigValue('xai-api-key', typed);
          clearCache('xai-api-key');
          break;
        }
        case 'claude': {
          const typed = nextList as ProviderKeyConfig[];
          claudeConfigsRef.current = typed;
          setClaudeConfigs(typed);
          updateConfigValue('claude-api-key', typed);
          clearCache('claude-api-key');
          break;
        }
        case 'vertex': {
          const typed = nextList as ProviderKeyConfig[];
          vertexConfigsRef.current = typed;
          setVertexConfigs(typed);
          updateConfigValue('vertex-api-key', typed);
          clearCache('vertex-api-key');
          break;
        }
        case 'openai': {
          const typed = nextList as OpenAIProviderConfig[];
          openaiProvidersRef.current = typed;
          setOpenaiProviders(typed);
          updateConfigValue('openai-compatibility', typed);
          clearCache('openai-compatibility');
          break;
        }
      }
    },
    [clearCache, updateConfigValue]
  );

  const syncAllListsEtag = useCallback(
    (etag?: string, options?: { excludeKind?: ProviderKind }) => {
      const normalized = typeof etag === 'string' ? etag.trim() : '';
      if (!normalized) return;
      const current = getCurrentProviderLists();
      const kinds: ProviderKind[] = [
        'gemini',
        'interactions',
        'codex',
        'xai',
        'claude',
        'vertex',
        'openai',
      ];
      kinds.forEach((kind) => {
        if (options?.excludeKind === kind) return;
        const list = current[kind];
        let changed = false;
        const patched = list.map((item) => {
          if (!item?.v8Source || item.v8Source.etag === normalized) return item;
          changed = true;
          return {
            ...item,
            v8Source: {
              ...item.v8Source,
              etag: normalized,
            },
          };
        });
        if (changed) {
          applySingleFamilyState(kind, patched as ProviderListsSnapshot[ProviderKind]);
        }
      });
    },
    [applySingleFamilyState, getCurrentProviderLists]
  );

  const getErrorMessage = useCallback(
    (err: unknown) => {
      const code = getUsageServiceErrorCode(err);
      if (code) {
        return t(`usage_service_errors.${code}`, {
          defaultValue: t('usage_service_errors.request_failed'),
        });
      }
      if (err instanceof Error) return err.message;
      if (typeof err === 'string') return err;
      return '';
    },
    [t]
  );

  const loadConfigs = useCallback(
    async (options?: {
      force?: boolean;
      onRowsBuilt?: (nextRows: ProviderRow[]) => void;
      preserveErrorOnFailure?: boolean;
    }): Promise<ProviderRow[] | null> => {
      const force = Boolean(options?.force);
      const hasValidCache = !force && isCacheValid();
      if (!hasValidCache && !force) {
        setLoading(true);
      }
      if (!options?.preserveErrorOnFailure) {
        setError('');
      }
      try {
        const snapshotLoader =
          'loadAllProvidersV8Snapshot' in providersApi &&
          typeof providersApi.loadAllProvidersV8Snapshot === 'function'
            ? providersApi.loadAllProvidersV8Snapshot
            : undefined;

        const [configResult, snapshotResult, vertexResult, openaiResult] = await Promise.allSettled([
          fetchConfig(undefined, force),
          force && snapshotLoader ? snapshotLoader() : Promise.resolve(null),
          providersApi.getVertexConfigs(),
          providersApi.getOpenAIProviders(),
        ]);

        if (configResult.status !== 'fulfilled') {
          throw configResult.reason;
        }

        const data = configResult.value;
        let nextGemini = data?.geminiApiKeys || [];
        let nextInteractions = data?.interactionsApiKeys || [];
        let nextCodex = data?.codexApiKeys || [];
        let nextXAI = data?.xaiApiKeys || [];
        let nextClaude = data?.claudeApiKeys || [];
        let nextVertex = data?.vertexApiKeys || [];
        let nextOpenAI = data?.openaiCompatibility || [];

        const hasSnapshot =
          snapshotResult.status === 'fulfilled' && Boolean(snapshotResult.value);

        if (hasSnapshot && snapshotResult.value) {
          supersededConfigRef.current = data ?? null;
          v8AliasSupportedRef.current = snapshotResult.value.v8Supported;
          setV8AliasSupported(snapshotResult.value.v8Supported);
          nextGemini = snapshotResult.value.gemini;
          nextInteractions = snapshotResult.value.interactions;
          nextCodex = snapshotResult.value.codex;
          nextXAI = snapshotResult.value.xai;
          nextClaude = snapshotResult.value.claude;
          nextVertex = snapshotResult.value.vertex;
          nextOpenAI = snapshotResult.value.openai;
        } else {
          supersededConfigRef.current = null;
          if (data?.v8Supported !== true) {
            if (vertexResult.status === 'fulfilled') {
              nextVertex = vertexResult.value || [];
              if (!areProviderListsJsonEqual(nextVertex, data?.vertexApiKeys || [])) {
                updateConfigValue('vertex-api-key', nextVertex);
              }
            }

            if (openaiResult.status === 'fulfilled') {
              nextOpenAI = openaiResult.value || [];
              if (!areProviderListsJsonEqual(nextOpenAI, data?.openaiCompatibility || [])) {
                updateConfigValue('openai-compatibility', nextOpenAI);
              }
            }
          }
          if (typeof data?.v8Supported === 'boolean') {
            v8AliasSupportedRef.current = data.v8Supported;
            setV8AliasSupported(data.v8Supported);
          } else {
            const anyV8Tag = hasAnyV8SourceTag({
              gemini: nextGemini,
              interactions: nextInteractions,
              codex: nextCodex,
              xai: nextXAI,
              claude: nextClaude,
              vertex: nextVertex,
              openai: nextOpenAI,
            });
            v8AliasSupportedRef.current = anyV8Tag;
            setV8AliasSupported(anyV8Tag);
          }
        }

        const nextLists: ProviderListsSnapshot = {
          gemini: nextGemini,
          interactions: nextInteractions,
          codex: nextCodex,
          xai: nextXAI,
          claude: nextClaude,
          vertex: nextVertex,
          openai: nextOpenAI,
        };
        applyProviderListsState(nextLists);

        const nextRows = buildProviderRows({
          ...nextLists,
          usageByProvider: usageByProviderRef.current,
          historyByRowKey: historyByRowKeyRef.current,
        });
        options?.onRowsBuilt?.(nextRows);
        return nextRows;
      } catch (err: unknown) {
        if (!options?.preserveErrorOnFailure) {
          const message = getErrorMessage(err) || t('notification.refresh_failed');
          setError(message);
        }
        return null;
      } finally {
        setLoading(false);
      }
    },
    [
      applyProviderListsState,
      fetchConfig,
      getErrorMessage,
      isCacheValid,
      t,
      updateConfigValue,
    ]
  );

  useEffect(() => {
    if (hasMounted.current) return;
    hasMounted.current = true;
    void loadConfigs();
  }, [loadConfigs]);

  const leftCurrentLayerRef = useRef(false);

  useEffect(() => {
    if (!isCurrentLayer) {
      leftCurrentLayerRef.current = true;
      return;
    }
    if (leftCurrentLayerRef.current) {
      leftCurrentLayerRef.current = false;
      if (!isCacheValid()) {
        void loadConfigs();
      }
    }
    void loadRecentRequests().catch(() => {});
  }, [isCurrentLayer, isCacheValid, loadConfigs, loadRecentRequests]);

  useEffect(() => {
    const superseded = supersededConfigRef.current;
    if (config?.geminiApiKeys && config.geminiApiKeys !== superseded?.geminiApiKeys) {
      setGeminiKeys(config.geminiApiKeys);
    }
    if (
      config?.interactionsApiKeys &&
      config.interactionsApiKeys !== superseded?.interactionsApiKeys
    ) {
      setInteractionsKeys(config.interactionsApiKeys);
    }
    if (config?.codexApiKeys && config.codexApiKeys !== superseded?.codexApiKeys) {
      setCodexConfigs(config.codexApiKeys);
    }
    if (config?.xaiApiKeys && config.xaiApiKeys !== superseded?.xaiApiKeys) {
      setXAIConfigs(config.xaiApiKeys);
    }
    if (config?.claudeApiKeys && config.claudeApiKeys !== superseded?.claudeApiKeys) {
      setClaudeConfigs(config.claudeApiKeys);
    }
    if (config?.vertexApiKeys && config.vertexApiKeys !== superseded?.vertexApiKeys) {
      setVertexConfigs(config.vertexApiKeys);
    }
    if (
      config?.openaiCompatibility &&
      config.openaiCompatibility !== superseded?.openaiCompatibility
    ) {
      setOpenaiProviders(config.openaiCompatibility);
    }
  }, [
    config?.geminiApiKeys,
    config?.interactionsApiKeys,
    config?.codexApiKeys,
    config?.xaiApiKeys,
    config?.claudeApiKeys,
    config?.vertexApiKeys,
    config?.openaiCompatibility,
  ]);

  const handleWorkspaceRefresh = useCallback(async () => {
    setRefreshing(true);
    try {
      await Promise.all([
        loadConfigs({ force: true }),
        refreshRecentRequests(),
        refreshAccountHistory(),
      ]);
    } finally {
      setRefreshing(false);
    }
  }, [loadConfigs, refreshAccountHistory, refreshRecentRequests]);

  useHeaderRefresh(handleWorkspaceRefresh, isCurrentLayer);

  const rows = useMemo(
    () =>
      buildProviderRows({
        gemini: geminiKeys,
        interactions: interactionsKeys,
        codex: codexConfigs,
        xai: xaiConfigs,
        claude: claudeConfigs,
        vertex: vertexConfigs,
        openai: openaiProviders,
        usageByProvider,
        historyByRowKey,
      }),
    [
      claudeConfigs,
      codexConfigs,
      geminiKeys,
      historyByRowKey,
      interactionsKeys,
      openaiProviders,
      usageByProvider,
      vertexConfigs,
      xaiConfigs,
    ]
  );

  const allModelNames = useMemo(() => {
    const names = new Set<string>();
    rows.forEach((row) => {
      (row.modelFilterKeys ?? row.modelNames).forEach((name) => names.add(name));
    });
    return Array.from(names).sort();
  }, [rows]);

  useEffect(() => {
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
        statusFilter,
        healthFilter,
        searchText,
        selectedModels,
        sortOption,
        sortDirection,
      }),
    [
      healthFilter,
      kindFilter,
      rows,
      searchText,
      selectedModels,
      sortDirection,
      sortOption,
      statusFilter,
    ]
  );

  const rowsRef = useRef(rows);
  rowsRef.current = rows;

  const {
    page: currentPage,
    setPage,
    pageSize,
    setPageSize,
    totalPages,
    startItem: pageStartItem,
    endItem: pageEndItem,
    pageItems: pagedRows,
  } = useClientPagination(visibleRows, {
    pageSize: storedPageSize,
    onPageSizeChange: setStoredPageSize,
    resetDeps: [
      healthFilter,
      kindFilter,
      searchText,
      selectedModels,
      sortDirection,
      sortOption,
      statusFilter,
    ],
  });

  const {
    selectionMode,
    setSelectionMode,
    toggleSelectionMode: handleToggleSelectionMode,
    selectedKeys,
    selectedItems: selectedRows,
    toggleItem: handleToggleSelectRow,
    togglePage: handleToggleSelectAllPage,
    selectAll: handleSelectAllFiltered,
    invertSelection,
    clearSelection,
    removeKeys: removeSelectedKeys,
  } = useListSelection({
    items: visibleRows,
    pageItems: pagedRows,
    getKey: (row) => row.key,
  });
  const selectedRowsRef = useRef(selectedRows);
  selectedRowsRef.current = selectedRows;
  const selectedKeysRef = useRef(selectedKeys);
  selectedKeysRef.current = selectedKeys;
  const kindCounts = useMemo(() => {
    const counts: Record<ProviderKindFilter, number> = {
      all: rows.length,
      gemini: 0,
      interactions: 0,
      codex: 0,
      xai: 0,
      claude: 0,
      vertex: 0,
      openai: 0,
    };
    rows.forEach((row) => {
      counts[row.kind] += 1;
    });
    return counts;
  }, [rows]);

  const activeDrawerRow = useMemo(() => {
    if (!drawerRowKey) return null;
    const exactMatch = rows.find((row) => row.key === drawerRowKey) ?? null;
    if (!exactMatch) return null;
    if (
      !drawerReanchoring &&
      !isSwitching &&
      drawerRowSnapshot &&
      drawerRowSnapshot.key === drawerRowKey &&
      !normalizeIdentityText(exactMatch.raw.authIndex) &&
      !matchesDistinguishingRowIdentity(drawerRowSnapshot, exactMatch)
    ) {
      return null;
    }
    return exactMatch;
  }, [drawerReanchoring, drawerRowKey, drawerRowSnapshot, isSwitching, rows]);

  const resolvedDrawerRow =
    drawerOpen && drawerMode === 'view-edit'
      ? (activeDrawerRow ?? drawerOrphanedRow ?? drawerRowSnapshot)
      : null;
  const isDrawerRowDeletedExternally = Boolean(
    drawerOpen && drawerMode === 'view-edit' && drawerOrphanedRow && !activeDrawerRow
  );

  const closeDrawer = useCallback(() => {
    drawerOpenRef.current = false;
    drawerRowKeyRef.current = null;
    pendingSavedIdentityRef.current = null;
    orphanNotifiedKeyRef.current = null;
    setDrawerOrphanedRow(null);
    setDrawerRowSnapshot(null);
    setDrawerOpen(false);
    setDrawerRowKey(null);
  }, []);

  useEffect(() => {
    if (!drawerOpen || drawerMode !== 'view-edit') {
      pendingSavedIdentityRef.current = null;
      orphanNotifiedKeyRef.current = null;
      if (drawerRowSnapshot !== null) {
        setDrawerRowSnapshot(null);
      }
      if (drawerOrphanedRow !== null) {
        setDrawerOrphanedRow(null);
      }
      return;
    }
    if (activeDrawerRow) {
      const snapshotMatches =
        drawerRowSnapshot !== null &&
        drawerRowSnapshot.key === activeDrawerRow.key &&
        drawerRowSnapshot.raw === activeDrawerRow.raw &&
        drawerRowSnapshot.enabled === activeDrawerRow.enabled &&
        drawerRowSnapshot.originalIndex === activeDrawerRow.originalIndex;
      if (!snapshotMatches) {
        setDrawerRowSnapshot(activeDrawerRow);
      }
      if (drawerOrphanedRow !== null) {
        setDrawerOrphanedRow(null);
      }
      orphanNotifiedKeyRef.current = null;
      const pendingSave = pendingSavedIdentityRef.current;
      if (!drawerReanchoring && !isSwitching && pendingSave) {
        const isStalePreSaveRow =
          pendingSave.previousRow !== null &&
          activeDrawerRow.key === pendingSave.previousKey &&
          activeDrawerRow.raw === pendingSave.previousRow.raw;
        const targetName = normalizeIdentityText(pendingSave.savedIdentity.name);
        const targetApiKey = normalizeIdentityText(pendingSave.savedIdentity.apiKey);
        const targetBaseUrl = normalizeIdentityText(pendingSave.savedIdentity.baseUrl);
        const targetProxyUrl = normalizeIdentityText(pendingSave.savedIdentity.proxyUrl);
        const targetPrefix = normalizeIdentityText(pendingSave.savedIdentity.prefix);
        const matchesSavedFields =
          (!targetName || normalizeIdentityText(activeDrawerRow.raw.name) === targetName) &&
          (activeDrawerRow.kind === 'openai' ||
            !targetApiKey ||
            normalizeIdentityText(activeDrawerRow.raw.apiKey) === targetApiKey) &&
          (!targetBaseUrl || normalizeIdentityText(activeDrawerRow.baseUrl) === targetBaseUrl) &&
          (activeDrawerRow.kind === 'openai' ||
            !targetProxyUrl ||
            normalizeIdentityText(activeDrawerRow.raw.proxyUrl) === targetProxyUrl) &&
          (!targetPrefix || normalizeIdentityText(activeDrawerRow.prefix) === targetPrefix);
        const matchesSavedAnchor =
          pendingSave.kind === activeDrawerRow.kind &&
          rowMatchesSavedAnchor(activeDrawerRow, pendingSave.savedIdentity) &&
          matchesSavedFields;
        const satisfiedNonKeySave =
          pendingSave.kind === activeDrawerRow.kind &&
          pendingSave.previousRow !== null &&
          activeDrawerRow.key === pendingSave.previousKey &&
          activeDrawerRow.raw !== pendingSave.previousRow.raw &&
          matchesSavedFields;
        if (!isStalePreSaveRow && (matchesSavedAnchor || satisfiedNonKeySave)) {
          pendingSavedIdentityRef.current = null;
        }
      }
      return;
    }
    if (!drawerRowKey || loading || drawerReanchoring || isSwitching) {
      return;
    }

    const pendingSave = pendingSavedIdentityRef.current;
    const reanchoredFromPendingSave = pendingSave
      ? findReanchoredDrawerRow(
          rows,
          pendingSave.kind,
          pendingSave.previousRow,
          pendingSave.savedIdentity
        )
      : null;
    const reanchored = reanchoredFromPendingSave ?? findShiftedDrawerRow(rows, drawerRowSnapshot);
    if (reanchored) {
      pendingSavedIdentityRef.current = null;
      orphanNotifiedKeyRef.current = null;
      drawerRowKeyRef.current = reanchored.key;
      setDrawerRowKey(reanchored.key);
      setDrawerRowSnapshot(reanchored);
      setDrawerOrphanedRow(null);
      return;
    }

    const hasUnsavedDraft = Boolean(drawerControllerRef.current?.isDirty?.());
    if (hasUnsavedDraft) {
      const frozenSnapshot = drawerRowSnapshot ?? drawerOrphanedRow;
      if (frozenSnapshot && drawerOrphanedRow !== frozenSnapshot) {
        setDrawerOrphanedRow(frozenSnapshot);
      }
      if (orphanNotifiedKeyRef.current !== drawerRowKey) {
        orphanNotifiedKeyRef.current = drawerRowKey;
        showNotification(t('notification.provider_conflict_refresh'), 'warning');
      }
      return;
    }

    closeDrawer();
  }, [
    activeDrawerRow,
    closeDrawer,
    drawerMode,
    drawerOpen,
    drawerOrphanedRow,
    drawerReanchoring,
    drawerRowKey,
    drawerRowSnapshot,
    isSwitching,
    loading,
    rows,
    showNotification,
    t,
  ]);

  const filtersActive =
    kindFilter !== 'all' ||
    statusFilter !== 'all' ||
    healthFilter !== 'all' ||
    searchText.trim() !== '' ||
    selectedModels.size > 0;

  const clearFilters = useCallback(() => {
    setKindFilter('all');
    setStatusFilter('all');
    setHealthFilter('all');
    setSearchText('');
    setSelectedModels(new Set());
  }, []);

  const openProviderDrawer = useCallback(
    async (row: ProviderRow, tab: ProviderDrawerTab = 'overview'): Promise<boolean> => {
      const sameRow =
        drawerOpenRef.current &&
        drawerModeRef.current === 'view-edit' &&
        drawerRowKeyRef.current === row.key;
      if (drawerOpenRef.current && !sameRow && drawerControllerRef.current) {
        const allowed = await drawerControllerRef.current.requestCloseOrSwitch();
        if (!allowed) return false;
      }
      if (!sameRow) {
        setDrawerSessionId((n) => n + 1);
      }
      pendingSavedIdentityRef.current = null;
      orphanNotifiedKeyRef.current = null;
      drawerOpenRef.current = true;
      drawerModeRef.current = 'view-edit';
      drawerRowKeyRef.current = row.key;
      setDrawerOrphanedRow(null);
      setDrawerRowSnapshot(row);
      setDrawerMode('view-edit');
      setDrawerRowKey(row.key);
      setDrawerInitialTab(tab);
      setDrawerOpen(true);
      return true;
    },
    []
  );

  const openCreateDrawer = useCallback(
    async (preferredKind?: ProviderKind): Promise<boolean> => {
      if (drawerOpenRef.current && drawerControllerRef.current) {
        const allowed = await drawerControllerRef.current.requestCloseOrSwitch();
        if (!allowed) return false;
      }
      const resolvedKind = preferredKind ?? (kindFilter !== 'all' ? kindFilter : 'openai');
      setDrawerSessionId((n) => n + 1);
      pendingSavedIdentityRef.current = null;
      orphanNotifiedKeyRef.current = null;
      drawerOpenRef.current = true;
      drawerModeRef.current = 'create';
      drawerRowKeyRef.current = null;
      setDrawerOrphanedRow(null);
      setDrawerRowSnapshot(null);
      setDrawerMode('create');
      setDrawerCreateKind(resolvedKind);
      setDrawerRowKey(null);
      setDrawerInitialTab('config');
      setDrawerOpen(true);
      return true;
    },
    [kindFilter]
  );

  const handleDrawerSaved = useCallback(
    async (
      savedKind: ProviderKind,
      savedIdentity?: ProviderSavedIdentityHint
    ): Promise<boolean> => {
      const previousKey = drawerRowKeyRef.current;
      const previousRow = previousKey
        ? (rowsRef.current.find((candidate) => candidate.key === previousKey) ?? null)
        : null;
      const previousEtag = previousRow?.raw.v8Source?.etag;
      const shouldReanchor =
        drawerOpenRef.current &&
        drawerModeRef.current === 'view-edit' &&
        Boolean(previousKey) &&
        Boolean(savedIdentity);

      setDrawerReanchoring(true);
      pendingSavedIdentityRef.current =
        shouldReanchor && previousKey && savedIdentity
          ? {
              kind: savedKind,
              previousKey,
              previousRow,
              savedIdentity,
            }
          : null;

      try {
        clearCache();
        const refreshedRows = await loadConfigs({
          force: true,
          onRowsBuilt: (nextRows) => {
            if (!shouldReanchor || !previousKey || !savedIdentity) return;
            const existingAtPreviousKey = nextRows.find(
              (candidate) => candidate.key === previousKey
            );
            if (existingAtPreviousKey) {
              const targetName = normalizeIdentityText(savedIdentity.name);
              const targetApiKey = normalizeIdentityText(savedIdentity.apiKey);
              const targetBaseUrl = normalizeIdentityText(savedIdentity.baseUrl);
              const nameMismatch =
                Boolean(targetName) &&
                normalizeIdentityText(existingAtPreviousKey.raw.name) !== targetName;
              const apiKeyMismatch =
                existingAtPreviousKey.kind !== 'openai' &&
                Boolean(targetApiKey) &&
                normalizeIdentityText(existingAtPreviousKey.raw.apiKey) !== targetApiKey;
              const baseUrlMismatch =
                Boolean(targetBaseUrl) &&
                normalizeIdentityText(existingAtPreviousKey.baseUrl) !== targetBaseUrl;
              if (!nameMismatch && !apiKeyMismatch && !baseUrlMismatch) {
                pendingSavedIdentityRef.current = null;
                setDrawerRowSnapshot(existingAtPreviousKey);
                setDrawerOrphanedRow(null);
                return;
              }
            }
            const matched = findReanchoredDrawerRow(
              nextRows,
              savedKind,
              previousRow,
              savedIdentity
            );
            if (matched) {
              pendingSavedIdentityRef.current = null;
              drawerRowKeyRef.current = matched.key;
              setDrawerRowKey(matched.key);
              setDrawerRowSnapshot(matched);
              setDrawerOrphanedRow(null);
            }
          },
        });

        if (refreshedRows !== null) {
          pendingSavedIdentityRef.current = null;
        }

        const postSavedLists = getCurrentProviderLists();
        const kindEtag = findFirstV8EtagInList(postSavedLists[savedKind]);
        const latestReadEtag = readLatestV8ConfigEtag();
        const effectiveEtag =
          (latestReadEtag && latestReadEtag !== previousEtag ? latestReadEtag : undefined) ??
          (refreshedRows !== null ? kindEtag : undefined) ??
          savedIdentity?.v8Source?.etag ??
          kindEtag ??
          latestReadEtag;
        if (effectiveEtag) {
          syncAllListsEtag(
            effectiveEtag,
            refreshedRows === null ? { excludeKind: savedKind } : undefined
          );
        }

        return refreshedRows !== null;
      } finally {
        setDrawerReanchoring(false);
      }
    },
    [clearCache, getCurrentProviderLists, loadConfigs, syncAllListsEtag]
  );

  const commitChangedProviderLists = useCallback(
    async (
      previous: ProviderListsSnapshot,
      nextLists: ProviderListsSnapshot,
      changed: Record<ProviderKind, boolean>
    ) => {
      const hadV8SourceBefore = hasAnyV8SourceTag(previous);
      const seedEtag =
        findFirstV8EtagInLists(previous) ??
        findFirstV8EtagInLists(nextLists) ??
        readLatestV8ConfigEtag();
      let expectedEtag = seedEtag;
      let newestWriteEtag: string | undefined;
      let completedWrites = 0;

      const runFamilyWrite = async <T,>(
        kind: ProviderKind,
        nextList: T[],
        saveFn: (list: T[], options?: { expectedEtag?: string }) => Promise<unknown>
      ) => {
        if (!changed[kind]) return;
        const result = expectedEtag
          ? await saveFn(nextList, { expectedEtag })
          : await saveFn(nextList);
        completedWrites += 1;
        const nextEtag = extractWriteResultEtag(result) ?? readLatestV8ConfigEtag();
        if (nextEtag) {
          expectedEtag = nextEtag;
          newestWriteEtag = nextEtag;
        }
        applySingleFamilyState(kind, nextList as ProviderListsSnapshot[ProviderKind]);
      };

      try {
        await runFamilyWrite('gemini', nextLists.gemini, providersApi.saveGeminiKeys);
        await runFamilyWrite('interactions', nextLists.interactions, providersApi.saveInteractionsKeys);
        await runFamilyWrite('codex', nextLists.codex, providersApi.saveCodexConfigs);
        await runFamilyWrite('xai', nextLists.xai, providersApi.saveXAIConfigs);
        await runFamilyWrite('claude', nextLists.claude, providersApi.saveClaudeConfigs);
        await runFamilyWrite('vertex', nextLists.vertex, providersApi.saveVertexConfigs);
        await runFamilyWrite('openai', nextLists.openai, providersApi.saveOpenAIProviders);
      } catch (err: unknown) {
        if (err && typeof err === 'object') {
          Object.assign(err, { partialCommitted: completedWrites > 0 });
          throw err;
        }
        throw Object.assign(new Error(String(err)), {
          partialCommitted: completedWrites > 0,
        });
      }

      if (v8AliasSupportedRef.current && (hadV8SourceBefore || hasAnyV8SourceTag(nextLists))) {
        await loadConfigs({ force: true, preserveErrorOnFailure: true });
      }

      const afterRefreshLatest = readLatestV8ConfigEtag();
      const postRefreshLists = getCurrentProviderLists();
      const changedRefreshedEtag = findFirstV8EtagInChangedLists(postRefreshLists, changed);
      const effectiveEtag =
        (afterRefreshLatest && afterRefreshLatest !== seedEtag ? afterRefreshLatest : undefined) ??
        (changedRefreshedEtag && changedRefreshedEtag !== seedEtag
          ? changedRefreshedEtag
          : undefined) ??
        newestWriteEtag ??
        afterRefreshLatest;
      if (effectiveEtag) {
        syncAllListsEtag(effectiveEtag);
      }
    },
    [applySingleFamilyState, getCurrentProviderLists, loadConfigs, syncAllListsEtag]
  );

  const applyProviderEnabledActions = useCallback(
    async (actions: Map<string, ProviderHealthCheckApplyAction>) => {
      if (actions.size === 0) return;
      if (!beginConfigMutation('batch:enabled')) return;

      const rowByKey = new Map(rowsRef.current.map((row) => [row.key, row]));
      const previous = getCurrentProviderLists();
      const nextLists = cloneProviderLists(previous);
      const changed = createChangedMap();

      let appliedCount = 0;
      actions.forEach((action, providerKey) => {
        const row = rowByKey.get(providerKey);
        if (!row) return;
        const enabled = action === 'enable';
        if (row.enabled === enabled) return;

        if (row.kind === 'openai') {
          const current = nextLists.openai[row.originalIndex];
          if (!current) return;
          const nextEntry: OpenAIProviderConfig = { ...current };
          if (enabled) {
            delete nextEntry.disabled;
          } else {
            nextEntry.disabled = true;
          }
          nextLists.openai[row.originalIndex] = nextEntry;
          changed.openai = true;
          appliedCount += 1;
          return;
        }

        const targetList = nextLists[row.kind];
        const current = targetList[row.originalIndex];
        if (!current) return;
        const excludedModels = enabled
          ? withoutDisableAllModelsRule(current.excludedModels)
          : withDisableAllModelsRule(current.excludedModels);
        targetList[row.originalIndex] = { ...current, excludedModels };
        changed[row.kind] = true;
        appliedCount += 1;
      });

      if (appliedCount === 0) {
        finishConfigMutation();
        return;
      }

      applyProviderListsState(nextLists);

      try {
        await commitChangedProviderLists(previous, nextLists, changed);
        showNotification(t('notification.config_updated'), 'success');
      } catch (err: unknown) {
        if (!isPartialOrConflictError(err)) {
          applyProviderListsState(previous);
        }
        await loadConfigs({ force: true });
        showNotification(`${t('notification.update_failed')}: ${getErrorMessage(err)}`, 'error');
      } finally {
        finishConfigMutation();
      }
    },
    [
      applyProviderListsState,
      beginConfigMutation,
      commitChangedProviderLists,
      finishConfigMutation,
      getCurrentProviderLists,
      getErrorMessage,
      loadConfigs,
      showNotification,
      t,
    ]
  );

  const handleRowToggle = useCallback(
    (row: ProviderRow, enabled: boolean) => {
      const actionMap = new Map<string, ProviderHealthCheckApplyAction>([
        [row.key, enabled ? 'enable' : 'disable'],
      ]);
      void applyProviderEnabledActions(actionMap);
    },
    [applyProviderEnabledActions]
  );

  const handleRowPriorityChange = useCallback(
    async (row: ProviderRow, nextPriority?: number) => {
      const normalizedPriority =
        nextPriority !== undefined && Number.isFinite(nextPriority)
          ? Math.trunc(nextPriority)
          : undefined;
      if (row.priority === normalizedPriority) return;
      if (!beginConfigMutation(`${row.kind}:${row.originalIndex}:priority`)) return;

      const previous = getCurrentProviderLists();
      const nextLists = cloneProviderLists(previous);
      const changed = createChangedMap();

      const targetList = nextLists[row.kind] as Array<
        GeminiKeyConfig | ProviderKeyConfig | OpenAIProviderConfig
      >;
      const current = targetList[row.originalIndex];
      if (!current) {
        finishConfigMutation();
        return;
      }
      const nextItem = { ...current };
      if (normalizedPriority === undefined) {
        delete nextItem.priority;
      } else {
        nextItem.priority = normalizedPriority;
      }
      targetList[row.originalIndex] = nextItem;
      changed[row.kind] = true;

      try {
        await commitChangedProviderLists(previous, nextLists, changed);
        showNotification(t('notification.config_updated'), 'success');
      } catch (err: unknown) {
        if (isPartialOrConflictError(err)) {
          await loadConfigs({ force: true });
        } else {
          applyProviderListsState(previous);
        }
        showNotification(`${t('notification.update_failed')}: ${getErrorMessage(err)}`, 'error');
      } finally {
        finishConfigMutation();
      }
    },
    [
      applyProviderListsState,
      beginConfigMutation,
      commitChangedProviderLists,
      finishConfigMutation,
      getCurrentProviderLists,
      getErrorMessage,
      loadConfigs,
      showNotification,
      t,
    ]
  );

  const handleApplyBatchPriority = useCallback(async () => {
    const currentSelectedRows = selectedRowsRef.current;
    if (currentSelectedRows.length === 0) return;
    const trimmed = batchPriorityText.trim();
    let parsedPriority: number | undefined;
    if (trimmed !== '') {
      const numeric = Number(trimmed);
      if (!Number.isFinite(numeric)) {
        showNotification(
          t('ai_providers.priority_invalid', { defaultValue: '请输入有效的整数优先级' }),
          'error'
        );
        return;
      }
      parsedPriority = Math.trunc(numeric);
    }

    if (!beginConfigMutation('batch:priority')) return;
    const previous = getCurrentProviderLists();
    const nextLists = cloneProviderLists(previous);
    const changed = createChangedMap();

    currentSelectedRows.forEach((row) => {
      const targetList = nextLists[row.kind] as Array<
        GeminiKeyConfig | ProviderKeyConfig | OpenAIProviderConfig
      >;
      const current = targetList[row.originalIndex];
      if (!current) return;
      const nextEntry = { ...current };
      if (parsedPriority === undefined) {
        delete nextEntry.priority;
      } else {
        nextEntry.priority = parsedPriority;
      }
      targetList[row.originalIndex] = nextEntry;
      changed[row.kind] = true;
    });

    try {
      await commitChangedProviderLists(previous, nextLists, changed);
      setBatchPriorityModalOpen(false);
      setBatchPriorityText('');
      showNotification(t('notification.config_updated'), 'success');
    } catch (err: unknown) {
      if (!isPartialOrConflictError(err)) {
        applyProviderListsState(previous);
      }
      await loadConfigs({ force: true });
      showNotification(`${t('notification.update_failed')}: ${getErrorMessage(err)}`, 'error');
    } finally {
      finishConfigMutation();
    }
  }, [
    applyProviderListsState,
    batchPriorityText,
    beginConfigMutation,
    commitChangedProviderLists,
    finishConfigMutation,
    getCurrentProviderLists,
    getErrorMessage,
    loadConfigs,
    showNotification,
    t,
  ]);

  const handleRowDelete = useCallback(
    (row: ProviderRow) => {
      const target = row.label || row.sortName || row.baseUrl;
      const titleKey =
        row.kind === 'openai'
          ? 'ai_providers.openai_delete_title'
          : `ai_providers.${row.kind}_delete_title`;
      const confirmKey =
        row.kind === 'openai'
          ? 'ai_providers.openai_delete_confirm'
          : `ai_providers.${row.kind}_delete_confirm`;

      showConfirmation({
        title: t(titleKey, {
          defaultValue: `Delete ${PROVIDER_KIND_LABELS[row.kind]}`,
        }),
        message: t(confirmKey),
        variant: 'danger',
        confirmText: t('common.next'),
        secondConfirmation: buildProviderDeleteSecondConfirmation(
          t,
          PROVIDER_KIND_LABELS[row.kind],
          target
        ),
        onConfirm: async () => {
          const latestRow = findMatchingFreshRow(rowsRef.current, row);
          const effectiveRow = latestRow ?? row;
          if (!beginConfigMutation(`${effectiveRow.kind}:${effectiveRow.originalIndex}:delete`)) {
            return;
          }
          const previous = getCurrentProviderLists();
          try {
            if (!latestRow) {
              await loadConfigs({ force: true });
              return;
            }

            const currentList = previous[latestRow.kind];
            const freshIndex =
              latestRow.originalIndex >= 0 && latestRow.originalIndex < currentList.length
                ? latestRow.originalIndex
                : -1;
            const freshRaw = freshIndex >= 0 ? currentList[freshIndex] : latestRow.raw;
            const deleteMethod = getPerItemDeleteMethod(latestRow.kind);

            if (deleteMethod) {
              const seedEtag =
                findFirstV8EtagInLists(previous) ?? readLatestV8ConfigEtag();
              let deleteResult: unknown;
              if (latestRow.kind === 'openai') {
                const openaiItem = freshRaw as OpenAIProviderConfig;
                deleteResult = await (
                  deleteMethod as (
                    name: string,
                    v8Source?: OpenAIProviderConfig['v8Source']
                  ) => Promise<unknown>
                )(openaiItem.name, openaiItem.v8Source);
              } else {
                const keyItem = freshRaw as GeminiKeyConfig | ProviderKeyConfig;
                deleteResult = await (
                  deleteMethod as (
                    apiKey: string,
                    baseUrl?: string,
                    v8Source?: GeminiKeyConfig['v8Source']
                  ) => Promise<unknown>
                )(keyItem.apiKey, keyItem.baseUrl, keyItem.v8Source);
              }
              const deleteEtag =
                extractWriteResultEtag(deleteResult) ?? readLatestV8ConfigEtag();
              if (freshIndex >= 0) {
                applySingleFamilyState(
                  latestRow.kind,
                  currentList.filter((_, idx) => idx !== freshIndex) as ProviderListsSnapshot[ProviderKind]
                );
              }
              await loadConfigs({ force: true, preserveErrorOnFailure: true });
              const refreshedKindEtag = findFirstV8EtagInList(
                getCurrentProviderLists()[latestRow.kind]
              );
              const afterDeleteLatest = readLatestV8ConfigEtag();
              const effectiveEtag =
                (afterDeleteLatest && afterDeleteLatest !== seedEtag
                  ? afterDeleteLatest
                  : undefined) ??
                (refreshedKindEtag && refreshedKindEtag !== seedEtag
                  ? refreshedKindEtag
                  : undefined) ??
                deleteEtag ??
                afterDeleteLatest ??
                refreshedKindEtag;
              if (effectiveEtag) {
                syncAllListsEtag(effectiveEtag);
              }
            } else {
              const nextLists = cloneProviderLists(previous);
              if (freshIndex >= 0) {
                const remaining = currentList.filter((_, idx) => idx !== freshIndex);
                switch (latestRow.kind) {
                  case 'gemini':
                    nextLists.gemini = remaining as GeminiKeyConfig[];
                    break;
                  case 'interactions':
                    nextLists.interactions = remaining as GeminiKeyConfig[];
                    break;
                  case 'codex':
                    nextLists.codex = remaining as ProviderKeyConfig[];
                    break;
                  case 'xai':
                    nextLists.xai = remaining as ProviderKeyConfig[];
                    break;
                  case 'claude':
                    nextLists.claude = remaining as ProviderKeyConfig[];
                    break;
                  case 'vertex':
                    nextLists.vertex = remaining as ProviderKeyConfig[];
                    break;
                  case 'openai':
                    nextLists.openai = remaining as OpenAIProviderConfig[];
                    break;
                }
              }
              await commitChangedProviderLists(previous, nextLists, createChangedMap(latestRow.kind));
            }

            if (
              drawerOpenRef.current &&
              (drawerRowKeyRef.current === row.key || drawerRowKeyRef.current === latestRow.key)
            ) {
              closeDrawer();
            }

            removeSelectedKeys([row.key, latestRow.key]);
            showNotification(t('notification.config_updated'), 'success');
          } catch (err: unknown) {
            if (!isPartialOrConflictError(err)) {
              applyProviderListsState(previous);
            }
            await loadConfigs({ force: true });
            showNotification(`${t('notification.delete_failed')}: ${getErrorMessage(err)}`, 'error');
          } finally {
            finishConfigMutation();
          }
        },
      });
    },
    [
      applyProviderListsState,
      applySingleFamilyState,
      closeDrawer,
      beginConfigMutation,
      commitChangedProviderLists,
      finishConfigMutation,
      getCurrentProviderLists,
      getErrorMessage,
      loadConfigs,
      removeSelectedKeys,
      showConfirmation,
      showNotification,
      syncAllListsEtag,
      t,
    ]
  );

  const handleBatchDelete = useCallback(() => {
    const targetSnapshot = [...selectedRowsRef.current];
    if (targetSnapshot.length === 0) return;
    const summaryLabel = targetSnapshot
      .slice(0, 3)
      .map((row) => row.label)
      .join(', ');
    const targetText =
      targetSnapshot.length > 3 ? `${summaryLabel} (+${targetSnapshot.length - 3})` : summaryLabel;

    showConfirmation({
      title: t('ai_providers.batch_delete_title', {
        count: targetSnapshot.length,
        defaultValue: `批量删除 ${targetSnapshot.length} 条提供商配置`,
      }),
      message: t('ai_providers.batch_delete_confirm', {
        count: targetSnapshot.length,
        targets: targetText,
        defaultValue: `确定删除已选中的 ${targetSnapshot.length} 条上游提供商配置（${targetText}）吗？此操作不可撤销。`,
      }),
      variant: 'danger',
      confirmText: t('common.next'),
      secondConfirmation: buildProviderDeleteSecondConfirmation(
        t,
        t('ai_providers.batch_delete_scope', { defaultValue: '批量提供商配置' }),
        targetText
      ),
      onConfirm: async () => {
        if (!beginConfigMutation('batch:delete')) return;
        const previous = getCurrentProviderLists();
        const freshRows = rowsRef.current;
        const removeIndexesByKind: Record<ProviderKind, Set<number>> = {
          gemini: new Set(),
          interactions: new Set(),
          codex: new Set(),
          xai: new Set(),
          claude: new Set(),
          vertex: new Set(),
          openai: new Set(),
        };
        const deletedKeys = new Set<string>();

        targetSnapshot.forEach((snapshotRow) => {
          const matchedRow = findMatchingFreshRow(freshRows, snapshotRow);
          if (!matchedRow) return;
          removeIndexesByKind[matchedRow.kind].add(matchedRow.originalIndex);
          deletedKeys.add(snapshotRow.key);
          deletedKeys.add(matchedRow.key);
        });

        const nextLists: ProviderListsSnapshot = {
          gemini: previous.gemini.filter((_, idx) => !removeIndexesByKind.gemini.has(idx)),
          interactions: previous.interactions.filter(
            (_, idx) => !removeIndexesByKind.interactions.has(idx)
          ),
          codex: previous.codex.filter((_, idx) => !removeIndexesByKind.codex.has(idx)),
          xai: previous.xai.filter((_, idx) => !removeIndexesByKind.xai.has(idx)),
          claude: previous.claude.filter((_, idx) => !removeIndexesByKind.claude.has(idx)),
          vertex: previous.vertex.filter((_, idx) => !removeIndexesByKind.vertex.has(idx)),
          openai: previous.openai.filter((_, idx) => !removeIndexesByKind.openai.has(idx)),
        };
        const changed: Record<ProviderKind, boolean> = {
          gemini: removeIndexesByKind.gemini.size > 0,
          interactions: removeIndexesByKind.interactions.size > 0,
          codex: removeIndexesByKind.codex.size > 0,
          xai: removeIndexesByKind.xai.size > 0,
          claude: removeIndexesByKind.claude.size > 0,
          vertex: removeIndexesByKind.vertex.size > 0,
          openai: removeIndexesByKind.openai.size > 0,
        };

        try {
          await commitChangedProviderLists(previous, nextLists, changed);
          if (
            drawerOpenRef.current &&
            drawerRowKeyRef.current &&
            deletedKeys.has(drawerRowKeyRef.current)
          ) {
            closeDrawer();
          }
          clearSelection();
          setSelectionMode(false);
          showNotification(t('notification.config_updated'), 'success');
        } catch (err: unknown) {
          if (!isPartialOrConflictError(err)) {
            applyProviderListsState(previous);
          }
          await loadConfigs({ force: true });
          showNotification(`${t('notification.delete_failed')}: ${getErrorMessage(err)}`, 'error');
        } finally {
          finishConfigMutation();
        }
      },
    });
  }, [
    applyProviderListsState,
    clearSelection,
    closeDrawer,
    beginConfigMutation,
    commitChangedProviderLists,
    finishConfigMutation,
    getCurrentProviderLists,
    getErrorMessage,
    loadConfigs,
    setSelectionMode,
    showConfirmation,
    showNotification,
    t,
  ]);

  const handleInvertVisibleSelection = useCallback(() => {
    invertSelection('all');
  }, [invertSelection]);
  const emptyState =
    rows.length > 0 && kindFilter !== 'all' && kindCounts[kindFilter] === 0 ? (
      <EmptyState
        variant="canvas"
        title={t('ai_providers.kind_empty_title', { name: PROVIDER_KIND_LABELS[kindFilter] })}
        action={
          <Button size="sm" onClick={() => void openCreateDrawer(kindFilter)} disabled={actionsDisabled}>
            {t('ai_providers.add_kind_button', { name: PROVIDER_KIND_LABELS[kindFilter] })}
          </Button>
        }
      />
    ) : rows.length > 0 && filtersActive ? (
      <EmptyState
        variant="canvas"
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
        variant="canvas"
        title={t('ai_providers.table_empty_title')}
        description={t('ai_providers.table_empty_desc')}
        action={
          <Button size="sm" onClick={() => void openCreateDrawer()} disabled={actionsDisabled}>
            {t('ai_providers.add_button')}
          </Button>
        }
      />
    );

  const healthCheckTargetRows = useMemo(
    () => (healthCheckScope === 'selected' ? selectedRows : visibleRows),
    [healthCheckScope, selectedRows, visibleRows]
  );

  const sharedListProps = {
    rows: pagedRows,
    loading,
    actionsDisabled,
    toggleDisabled: actionsDisabled,
    resolvedTheme,
    emptyState,
    selectionMode,
    selectedKeys,
    onToggleSelectRow: handleToggleSelectRow,
    onToggleSelectAllPage: handleToggleSelectAllPage,
    selectedDrawerKey: drawerOpen && drawerMode === 'view-edit' ? drawerRowKey : null,
    onInspect: (row: ProviderRow) => void openProviderDrawer(row, 'overview'),
    onOpenModels: (row: ProviderRow) => void openProviderDrawer(row, 'models'),
    onOpenConnectivity: (row: ProviderRow) => void openProviderDrawer(row, 'connectivity'),
    onHealthCheckRow: (row: ProviderRow) => void openProviderDrawer(row, 'connectivity'),
    onEdit: (row: ProviderRow) => void openProviderDrawer(row, 'config'),
    onDelete: handleRowDelete,
    onToggleEnabled: handleRowToggle,
    onChangePriority: handleRowPriorityChange,
    routingStrategy: config ? config.routingStrategy || 'round-robin' : undefined,
    globalDisableCooling: config?.disableCooling,
  };
  return (
    <div className={styles.container}>
      <ProviderToolbar
        kind={kindFilter}
        kindCounts={kindCounts}
        onKindChange={setKindFilter}
        statusFilter={statusFilter}
        onStatusFilterChange={setStatusFilter}
        healthFilter={healthFilter}
        onHealthFilterChange={setHealthFilter}
        searchText={searchText}
        onSearchTextChange={setSearchText}
        allModelNames={allModelNames}
        selectedModels={selectedModels}
        onSelectedModelsChange={setSelectedModels}
        sortOption={sortOption}
        onSortOptionChange={setSortOption}
        sortDirection={sortDirection}
        onSortDirectionChange={setSortDirection}
        onResetFilters={clearFilters}
        totalCount={rows.length}
        filteredCount={visibleRows.length}
        disabled={actionsDisabled}
        resolvedTheme={resolvedTheme}
        onRefresh={() => void handleWorkspaceRefresh()}
        refreshing={refreshing || loading}
        onAdd={(kind) => void openCreateDrawer(kind)}
        onHealthCheck={() => {
          setHealthCheckScope('filtered');
          setHealthCheckOpen(true);
        }}
        healthCheckDisabled={visibleRows.length === 0}
      />

      {error && <div className="error-box">{error}</div>}

      <WorkspaceListPanel<ProviderLayoutMode>
        showHeader={rows.length > 0}
        selectionActive={selectionMode || selectedRows.length > 0}
        summary={
          filtersActive
            ? t('ai_providers.workspace_filtered_summary', {
                visible: visibleRows.length,
                total: rows.length,
                defaultValue: `匹配 ${visibleRows.length} / 共 ${rows.length} 条渠道`,
              })
            : t('ai_providers.workspace_total_summary', {
                count: rows.length,
                defaultValue: `共 ${rows.length} 条上游渠道`,
              })
        }
        hint={
          selectionMode
            ? t('ai_providers.workspace_selection_hint', {
                defaultValue: '勾选上游渠道后可批量测活、启用、停用、调整优先级或删除。',
              })
            : t('ai_providers.workspace_batch_hint', {
                defaultValue: '选择上游渠道后可批量测活、启用、停用、调整优先级或删除。',
              })
        }
        selectionBadge={
          selectionMode
            ? t('ai_providers.selected_count_badge', {
                count: selectedRows.length,
                defaultValue: `已选 ${selectedRows.length} 项`,
              })
            : undefined
        }
        selectionTools={
          selectionMode ? (
            <>
              <Button
                variant="ghost"
                size="xs"
                onClick={handleSelectAllFiltered}
                disabled={visibleRows.length === 0 || actionsDisabled}
              >
                {t('ai_providers.select_all_filtered', {
                  count: visibleRows.length,
                  defaultValue: `全选当前结果 (${visibleRows.length})`,
                })}
              </Button>
              <Button
                variant="ghost"
                size="xs"
                onClick={handleInvertVisibleSelection}
                disabled={visibleRows.length === 0 || actionsDisabled}
              >
                {t('ai_providers.invert_selection', { defaultValue: '反选' })}
              </Button>
              {selectedRows.length > 0 && (
                <Button
                  variant="ghost"
                  size="xs"
                  onClick={clearSelection}
                  disabled={actionsDisabled}
                >
                  {t('ai_providers.clear_selected', { defaultValue: '清空已选' })}
                </Button>
              )}
            </>
          ) : undefined
        }
        headerActions={
          <Button
            variant={selectionMode ? 'primary' : 'secondary'}
            size="sm"
            onClick={handleToggleSelectionMode}
            disabled={visibleRows.length === 0 && !selectionMode}
          >
            {selectionMode
              ? t('ai_providers.exit_selection_mode', { defaultValue: '退出选择' })
              : t('ai_providers.enter_selection_mode', { defaultValue: '批量选择' })}
          </Button>
        }
        viewMode={{
          idBase: 'ai-providers-view-mode',
          value: effectiveLayoutMode,
          onChange: setLayoutMode,
          tableId: 'table',
          cardsId: 'grid',
          tableLabel: t('ai_providers.view_mode_table', { defaultValue: '表格' }),
          cardsLabel: t('ai_providers.view_mode_grid', { defaultValue: '卡片' }),
          ariaLabel: t('ai_providers.view_mode_switcher', { defaultValue: '列表视图模式' }),
          hidden: viewSwitcherHidden,
        }}
        footer={
          <LocalizedPaginationFooter
            currentPage={currentPage}
            totalPages={totalPages}
            totalCount={visibleRows.length}
            startItem={pageStartItem}
            endItem={pageEndItem}
            pageSize={pageSize}
            pageSizeOptions={PROVIDER_WORKSPACE_PAGE_SIZE_OPTIONS}
            onPageChange={setPage}
            onPageSizeChange={setPageSize}
            disabled={loading}
          />
        }
      >
        {effectiveLayoutMode === 'grid' ? (
          <ProviderCardGrid {...sharedListProps} />
        ) : (
          <ProviderTable {...sharedListProps} />
        )}
      </WorkspaceListPanel>

      <FloatingActionDock
        open={selectionMode && selectedRows.length > 0}
        role="toolbar"
        ariaLabel={t('ai_providers.batch_dock_aria', { defaultValue: '提供商批量操作栏' })}
        leading={
          <span className={styles.dockLeading}>
            {t('ai_providers.selected_count_badge', {
              count: selectedRows.length,
              defaultValue: `已选 ${selectedRows.length} 项`,
            })}
          </span>
        }
      >
        <div className={styles.dockActions}>
          <Button
            variant="secondary"
            size="sm"
            disabled={actionsDisabled}
            onClick={() => {
              setHealthCheckScope('selected');
              setHealthCheckOpen(true);
            }}
          >
            {t('ai_providers.batch_health_check', { defaultValue: '测活所选' })}
          </Button>
          <Button
            variant="secondary"
            size="sm"
            disabled={actionsDisabled}
            onClick={() => {
              const actionMap = new Map<string, ProviderHealthCheckApplyAction>();
              selectedRows.forEach((row) => actionMap.set(row.key, 'enable'));
              void applyProviderEnabledActions(actionMap);
            }}
          >
            {t('ai_providers.batch_enable', { defaultValue: '批量启用' })}
          </Button>
          <Button
            variant="secondary"
            size="sm"
            className={styles.dockDangerButton}
            disabled={actionsDisabled}
            onClick={() => {
              const actionMap = new Map<string, ProviderHealthCheckApplyAction>();
              selectedRows.forEach((row) => actionMap.set(row.key, 'disable'));
              void applyProviderEnabledActions(actionMap);
            }}
          >
            {t('ai_providers.batch_disable', { defaultValue: '批量停用' })}
          </Button>
          <Button
            variant="secondary"
            size="sm"
            disabled={actionsDisabled}
            onClick={() => {
              setBatchPriorityText('');
              setBatchPriorityModalOpen(true);
            }}
          >
            {t('ai_providers.batch_set_priority', { defaultValue: '设优先级' })}
          </Button>
          <DropdownMenu
            ariaLabel={t('ai_providers.batch_more_actions', { defaultValue: '更多批量操作' })}
            triggerIcon={<IconMoreVertical size={16} />}
            disabled={actionsDisabled}
            items={[
              {
                key: 'batch-delete',
                label: t('ai_providers.batch_delete_action', {
                  count: selectedRows.length,
                  defaultValue: `删除已选 (${selectedRows.length})`,
                }),
                icon: <IconTrash2 size={15} />,
                tone: 'danger',
                onClick: handleBatchDelete,
              },
            ]}
          />
        </div>
      </FloatingActionDock>

      <Modal
        open={batchPriorityModalOpen}
        onClose={() => setBatchPriorityModalOpen(false)}
        title={t('ai_providers.batch_priority_modal_title', {
          count: selectedRows.length,
          defaultValue: `批量设置优先级（已选 ${selectedRows.length} 项）`,
        })}
        width={440}
        footer={
          <>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setBatchPriorityModalOpen(false)}
              disabled={isSwitching}
            >
              {t('common.cancel')}
            </Button>
            <Button
              variant="primary"
              size="sm"
              loading={isSwitching}
              onClick={() => void handleApplyBatchPriority()}
            >
              {t('common.save')}
            </Button>
          </>
        }
      >
        <div className={styles.priorityModalBody}>
          <p className={styles.priorityModalHint}>
            {t('ai_providers.batch_priority_modal_hint', {
              defaultValue: '数值越大优先级越高；留空并保存将清除所选渠道的显式优先级设置。',
            })}
          </p>
          <Input
            label={t('ai_providers.priority_label')}
            type="number"
            step={1}
            value={batchPriorityText}
            onChange={(event) => setBatchPriorityText(event.target.value)}
            placeholder={t('ai_providers.priority_placeholder', { defaultValue: '例如 10（留空清除）' })}
          />
        </div>
      </Modal>

      <ProviderWorkspaceDrawer
        ref={drawerControllerRef}
        open={drawerOpen}
        mode={drawerMode}
        row={resolvedDrawerRow}
        createKind={drawerCreateKind}
        onCreateKindChange={setDrawerCreateKind}
        initialTab={drawerInitialTab}
        v8AliasSupported={v8AliasSupported}
        usageByProvider={usageByProvider}
        resolvedTheme={resolvedTheme}
        rowDeletedExternally={isDrawerRowDeletedExternally}
        routingStrategy={config ? config.routingStrategy || 'round-robin' : undefined}
        globalDisableCooling={config?.disableCooling}
        sessionId={drawerSessionId}
        onClose={closeDrawer}
        onSaved={handleDrawerSaved}
        onToggleEnabled={handleRowToggle}
        onDelete={handleRowDelete}
        toggleDisabled={actionsDisabled}
        deleteDisabled={actionsDisabled}
        runWithMutationLock={runWithMutationLock}
      />

      <ProviderHealthCheckDrawer
        open={healthCheckOpen}
        rows={healthCheckTargetRows}
        allRows={rows}
        scopeMode={healthCheckScope}
        actionsDisabled={actionsDisabled}
        onApplyResultActions={applyProviderEnabledActions}
        onSetProviderEnabled={async (providerKey, enabled) => {
          await applyProviderEnabledActions(new Map([[providerKey, enabled ? 'enable' : 'disable']]));
        }}
        onOpenProviderConfig={async (targetRow) => {
          const switched = await openProviderDrawer(targetRow, 'config');
          if (switched) {
            setHealthCheckOpen(false);
          }
        }}
        onClose={() => setHealthCheckOpen(false)}
      />
    </div>
  );
}
