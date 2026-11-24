import {
  useCallback,
  useId,
  useImperativeHandle,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type Ref,
} from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Drawer } from '@/components/ui/Drawer';
import { SegmentedTabs, type SegmentedTabItem } from '@/components/ui/SegmentedTabs';
import { IconCopy } from '@/components/ui/icons';
import { useNotificationStore } from '@/stores';
import { copyToClipboard } from '@/utils/clipboard';
import { getProviderKindIcon, PROVIDER_KIND_LABELS } from '../ProviderTable/kindMeta';
import type { ProviderV8GroupSource } from '@/types';
import type { ProviderKind, ProviderRow } from '../ProviderTable/rowData';
import type { ProviderDrawerTab } from '../types';
import type { ProviderRecentUsageMap } from '../utils';
import { ProviderConfigurationTab } from './ProviderConfigurationTab';
import { ProviderConnectivityTab } from './ProviderConnectivityTab';
import { ProviderModelsTab } from './ProviderModelsTab';
import { ProviderOverviewTab } from './ProviderOverviewTab';
import {
  isSameProviderEntryAcrossIndexShift,
  rowMatchesSavedAnchor,
  useProviderDrawerEditor,
  type ProviderMutationLockRunner,
} from './useProviderDrawerEditor';
import styles from './ProviderWorkspaceDrawer.module.scss';

export interface ProviderSavedIdentityHint {
  name?: string;
  apiKey?: string;
  baseUrl?: string;
  proxyUrl?: string;
  prefix?: string;
  authIndex?: string;
  v8Source?: ProviderV8GroupSource;
  originalIndex?: number;
}

export interface ProviderWorkspaceDrawerHandle {
  requestCloseOrSwitch: () => Promise<boolean>;
  isDirty?: () => boolean;
}

export interface ProviderWorkspaceDrawerProps {
  open: boolean;
  mode: 'view-edit' | 'create';
  row: ProviderRow | null;
  createKind?: ProviderKind;
  supportedKinds?: ReadonlySet<ProviderKind>;
  onCreateKindChange?: (kind: ProviderKind) => void;
  initialTab?: ProviderDrawerTab;
  v8AliasSupported?: boolean;
  usageByProvider: ProviderRecentUsageMap;
  resolvedTheme?: string;
  actionsDisabled?: boolean;
  rowDeletedExternally?: boolean;
  sessionId?: string | number;
  onClose: () => void;
  onSaved: (
    kind: ProviderKind,
    savedIdentity?: ProviderSavedIdentityHint
  ) => Promise<boolean | void> | boolean | void;
  onToggleEnabled?: (row: ProviderRow, enabled: boolean) => void;
  onDelete?: (row: ProviderRow) => void;
  toggleDisabled?: boolean;
  deleteDisabled?: boolean;
  runWithMutationLock?: ProviderMutationLockRunner;
  ref?: Ref<ProviderWorkspaceDrawerHandle>;
}

const CANONICAL_PROVIDER_KINDS: ProviderKind[] = [
  'openai',
  'claude',
  'codex',
  'xai',
  'gemini',
  'interactions',
  'vertex',
];

const resolveInitialTab = (
  mode: 'view-edit' | 'create',
  initialTab?: ProviderDrawerTab
): ProviderDrawerTab => {
  if (mode === 'create') {
    return initialTab && initialTab !== 'overview' ? initialTab : 'config';
  }
  return initialTab ?? 'overview';
};

export function ProviderWorkspaceDrawer({
  open,
  mode,
  row,
  createKind,
  supportedKinds,
  onCreateKindChange,
  initialTab,
  v8AliasSupported,
  usageByProvider,
  resolvedTheme,
  actionsDisabled = false,
  rowDeletedExternally = false,
  sessionId,
  onClose,
  onSaved,
  onToggleEnabled,
  onDelete,
  toggleDisabled,
  deleteDisabled,
  runWithMutationLock,
  ref,
}: ProviderWorkspaceDrawerProps) {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);

  const availableCreateKinds = useMemo(() => {
    if (!supportedKinds || supportedKinds.size === 0) {
      return CANONICAL_PROVIDER_KINDS;
    }
    const filtered = CANONICAL_PROVIDER_KINDS.filter((kind) => supportedKinds.has(kind));
    return filtered.length ? filtered : CANONICAL_PROVIDER_KINDS;
  }, [supportedKinds]);

  const [internalCreateKind, setInternalCreateKind] = useState<ProviderKind>(
    () => createKind ?? availableCreateKinds[0] ?? 'openai'
  );

  const effectiveCreateKind = createKind ?? internalCreateKind;
  const activeKind: ProviderKind =
    mode === 'view-edit' && row ? row.kind : effectiveCreateKind;

  const [prevOpen, setPrevOpen] = useState(open);
  const [openEpoch, setOpenEpoch] = useState(() => (open ? 1 : 0));
  const [resetNonce, setResetNonce] = useState(0);
  if (prevOpen !== open) {
    setPrevOpen(open);
    if (open) {
      setOpenEpoch((n) => n + 1);
    }
  }

  const [rowSession, setRowSession] = useState<{
    token: number;
    open: boolean;
    mode: 'view-edit' | 'create';
    sessionId?: string | number;
    row: ProviderRow | null;
  }>(() => ({
    token: open ? 1 : 0,
    open,
    mode,
    sessionId,
    row,
  }));

  const [tabState, setTabState] = useState<{
    sessionKey: string;
    tab: ProviderDrawerTab;
    reanchor?: {
      openEpoch: number;
      sessionToken: number;
      fromRowKey: string | null;
      kind: ProviderKind;
      initialTab: ProviderDrawerTab | undefined;
      tab: ProviderDrawerTab;
      savedIdentity?: ProviderSavedIdentityHint;
    };
  }>(() => ({
    sessionKey: open ? `${openEpoch}:${open ? 1 : 0}:${mode}:${initialTab ?? ''}` : 'closed',
    tab: resolveInitialTab(mode, initialTab),
  }));

  let currentRowSessionToken = rowSession.token;
  let sessionChanged = false;
  if (rowSession.open !== open || rowSession.mode !== mode) {
    sessionChanged = true;
  } else if (open) {
    if (sessionId !== undefined) {
      sessionChanged = rowSession.sessionId !== sessionId;
    } else if (mode === 'view-edit' && rowSession.row?.key !== row?.key) {
      const matchesReanchor = Boolean(
        tabState.reanchor &&
          tabState.reanchor.openEpoch === openEpoch &&
          tabState.reanchor.sessionToken === rowSession.token &&
          row !== null &&
          rowMatchesSavedAnchor(row, tabState.reanchor.savedIdentity)
      );
      const matchesShift = isSameProviderEntryAcrossIndexShift(rowSession.row, row);
      sessionChanged = !matchesReanchor && !matchesShift;
    }
  }
  if (
    sessionChanged ||
    rowSession.open !== open ||
    rowSession.mode !== mode ||
    rowSession.sessionId !== sessionId ||
    rowSession.row !== row
  ) {
    const nextToken = sessionChanged ? rowSession.token + 1 : rowSession.token;
    currentRowSessionToken = nextToken;
    setRowSession({
      token: nextToken,
      open,
      mode,
      sessionId,
      row,
    });
  }

  const sessionKey = open
    ? `${openEpoch}:${currentRowSessionToken}:${mode}:${initialTab ?? ''}`
    : 'closed';
  const canPreserveAcrossSave =
    open &&
    mode === 'view-edit' &&
    Boolean(tabState.reanchor) &&
    tabState.reanchor?.openEpoch === openEpoch &&
    tabState.reanchor?.sessionToken === currentRowSessionToken &&
    tabState.reanchor?.kind === activeKind &&
    (tabState.reanchor?.initialTab ?? '') === (initialTab ?? '') &&
    (row?.key === tabState.reanchor?.fromRowKey ||
      (row !== null && rowMatchesSavedAnchor(row, tabState.reanchor?.savedIdentity)));
  const activeTab =
    tabState.sessionKey === sessionKey
      ? tabState.tab
      : canPreserveAcrossSave && tabState.reanchor
        ? tabState.reanchor.tab
        : resolveInitialTab(mode, initialTab);

  if (
    tabState.reanchor &&
    open &&
    mode === 'view-edit' &&
    tabState.reanchor.openEpoch === openEpoch &&
    tabState.reanchor.sessionToken === currentRowSessionToken &&
    row !== null &&
    row.key !== tabState.reanchor.fromRowKey &&
    rowMatchesSavedAnchor(row, tabState.reanchor.savedIdentity)
  ) {
    setTabState({ sessionKey, tab: activeTab });
  }

  const handleEditorSaved = useCallback(
    async (savedKind: ProviderKind, savedIdentity?: ProviderSavedIdentityHint) => {
      setTabState({
        sessionKey,
        tab: activeTab,
        reanchor: {
          openEpoch,
          sessionToken: currentRowSessionToken,
          fromRowKey: row?.key ?? null,
          kind: savedKind,
          initialTab,
          tab: activeTab,
          savedIdentity,
        },
      });
      try {
        return await onSaved(savedKind, savedIdentity);
      } catch (err) {
        setTabState((prev) => (prev.reanchor ? { sessionKey: prev.sessionKey, tab: prev.tab } : prev));
        throw err;
      }
    },
    [activeTab, currentRowSessionToken, initialTab, onSaved, openEpoch, row?.key, sessionKey]
  );

  const tabsIdPrefix = useId();
  const tabRefs = useRef<Partial<Record<ProviderDrawerTab, HTMLButtonElement | null>>>({});
  const bodyRef = useRef<HTMLDivElement | null>(null);
  const editor = useProviderDrawerEditor({
    open,
    mode,
    row,
    activeKind,
    v8AliasSupported,
    actionsDisabled,
    rowDeletedExternally,
    sessionIdentity: open ? `${openEpoch}:${currentRowSessionToken}:${mode}` : 'closed',
    onClose,
    onSaved: handleEditorSaved,
    runWithMutationLock,
  });

  const { resetForm: resetEditorForm } = editor;
  const handleResetForm = useCallback(() => {
    resetEditorForm();
    setResetNonce((n) => n + 1);
  }, [resetEditorForm]);

  const effectiveTheme = useMemo<'light' | 'dark'>(() => {
    if (resolvedTheme === 'dark') return 'dark';
    if (resolvedTheme === 'light') return 'light';
    if (typeof document !== 'undefined') {
      return document.documentElement?.getAttribute('data-theme') === 'dark' ? 'dark' : 'light';
    }
    return 'light';
  }, [resolvedTheme]);

  const switchTab = useCallback(
    (nextTab: ProviderDrawerTab) => {
      setTabState({ sessionKey, tab: nextTab });
      if (typeof bodyRef.current?.scrollTo === 'function') {
        bodyRef.current.scrollTo({ top: 0 });
      }
    },
    [sessionKey]
  );

  const handleTabKeyDown = useCallback(
    (
      event: KeyboardEvent<HTMLButtonElement>,
      currentIndex: number,
      tabs: Array<{ key: ProviderDrawerTab }>
    ) => {
      if (tabs.length === 0) return;
      let nextIndex: number | null = null;
      if (event.key === 'ArrowRight') {
        nextIndex = (currentIndex + 1) % tabs.length;
      } else if (event.key === 'ArrowLeft') {
        nextIndex = (currentIndex - 1 + tabs.length) % tabs.length;
      } else if (event.key === 'Home') {
        nextIndex = 0;
      } else if (event.key === 'End') {
        nextIndex = tabs.length - 1;
      }
      if (nextIndex === null) return;
      const targetTab = tabs[nextIndex];
      if (!targetTab) return;
      event.preventDefault();
      switchTab(targetTab.key);
      tabRefs.current[targetTab.key]?.focus();
    },
    [switchTab]
  );
  const requestCloseOrSwitch = useCallback(async () => {
    const allowed = await editor.confirmDiscardChanges();
    if (allowed) {
      setTabState((prev) => (prev.reanchor ? { sessionKey: prev.sessionKey, tab: prev.tab } : prev));
    }
    return allowed;
  }, [editor]);


  useImperativeHandle(
    ref,
    () => ({
      requestCloseOrSwitch,
      isDirty: () => editor.isDirty,
    }),
    [editor.isDirty, requestCloseOrSwitch]
  );

  const handleRequestClose = useCallback(async () => {
    const allowed = await requestCloseOrSwitch();
    if (allowed) {
      onClose();
    }
  }, [onClose, requestCloseOrSwitch]);

  const handleSwitchCreateKind = useCallback(
    async (nextKind: ProviderKind) => {
      if (nextKind === effectiveCreateKind) return;
      const confirmed = await editor.confirmDiscardChanges();
      if (!confirmed) return;
      setTabState((prev) => (prev.reanchor ? { sessionKey: prev.sessionKey, tab: prev.tab } : prev));
      setInternalCreateKind(nextKind);
      onCreateKindChange?.(nextKind);
    },
    [editor, effectiveCreateKind, onCreateKindChange]
  );

  const handleOpenAIKeyCountChange = useCallback(
    (action: 'add' | 'remove', index?: number) => {
      editor.setOpenAIKeyStatuses((prev) => {
        if (action === 'add') {
          return [...prev, { status: 'idle', message: '' }];
        }
        if (typeof index === 'number') {
          return prev.filter((_, idx) => idx !== index);
        }
        return prev;
      });
    },
    [editor]
  );

  const handleCopyHost = async () => {
    const copyValue = editor.form.baseUrl.trim() || row?.baseUrl || row?.hostLabel || '';
    if (!copyValue) return;
    const copied = await copyToClipboard(copyValue);
    if (copied) {
      showNotification(t('common.copied_to_clipboard', { label: 'Base URL' }), 'success');
    }
  };

  const kindLabel = PROVIDER_KIND_LABELS[activeKind];
  const hostDisplay = (() => {
    if (mode === 'view-edit' && row?.hostLabel) return row.hostLabel;
    const trimmed = editor.form.baseUrl.trim();
    if (!trimmed) return '';
    try {
      return new URL(trimmed).host || trimmed;
    } catch {
      return trimmed;
    }
  })();
  const primaryTitle =
    mode === 'create'
      ? t('ai_providers.drawer_create_title', { defaultValue: '添加提供商配置' })
      : editor.form.name.trim() || row?.label || kindLabel;

  const createKindItems = useMemo<SegmentedTabItem<ProviderKind>[]>(
    () =>
      availableCreateKinds.map((kind) => ({
        id: kind,
        label: (
          <span className={styles.kindOptionLabel}>
            <img src={getProviderKindIcon(kind, effectiveTheme)} alt="" />
            <span>{PROVIDER_KIND_LABELS[kind]}</span>
          </span>
        ),
      })),
    [availableCreateKinds, effectiveTheme]
  );
  const headerTitleNode = (
    <div className={styles.drawerTitleIdentity}>
      <div className={styles.drawerTitleIcon}>
        <img src={getProviderKindIcon(activeKind, effectiveTheme)} alt="" />
      </div>
      <div className={styles.drawerTitleStack}>
        <strong className={styles.drawerTitlePrimary}>{primaryTitle}</strong>
        <div className={styles.drawerTitleMeta}>
          <span>{kindLabel}</span>
          {mode === 'view-edit' && row && (
            <>
              <span className={styles.metaSeparator} aria-hidden="true">
                ·
              </span>
              <span
                className={`${styles.statusBadge} ${
                  row.enabled ? styles.statusBadgeSuccess : styles.statusBadgeDisabled
                }`}
              >
                {row.enabled
                  ? t('ai_providers.status_enabled', { defaultValue: '已启用' })
                  : t('ai_providers.status_disabled', { defaultValue: '已停用' })}
              </span>
            </>
          )}
          {hostDisplay && (
            <>
              <span className={styles.metaSeparator} aria-hidden="true">
                ·
              </span>
              <button
                type="button"
                className={styles.hostCopyBtn}
                onClick={() => void handleCopyHost()}
                title={editor.form.baseUrl.trim() || row?.baseUrl || hostDisplay}
              >
                <span>{hostDisplay}</span>
                <IconCopy size={12} />
              </button>
            </>
          )}
          {editor.isDirty && (
            <span className={styles.dirtyBadge}>
              {t('ai_providers.drawer_unsaved_changes', { defaultValue: '未保存更改' })}
            </span>
          )}
        </div>
      </div>
    </div>
  );

  const footerNode =
    mode === 'create' ? (
      <div className={styles.drawerFooter}>
        <div className={styles.drawerFooterRight}>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => void handleRequestClose()}
            disabled={editor.saving}
          >
            {t('common.cancel')}
          </Button>
          <Button
            variant="primary"
            size="sm"
            onClick={() => void editor.handleSave()}
            loading={editor.saving}
            disabled={!editor.canSave}
          >
            {t('ai_providers.drawer_create_submit', { defaultValue: '创建配置' })}
          </Button>
        </div>
      </div>
    ) : (
      <div className={styles.drawerFooter}>
        <div className={styles.drawerFooterLeft}>
          {!editor.isDirty && onToggleEnabled && row && (
            <Button
              variant="secondary"
              size="sm"
              onClick={() => onToggleEnabled(row, !row.enabled)}
              disabled={actionsDisabled || toggleDisabled || editor.saving || rowDeletedExternally}
            >
              {row.enabled
                ? t('ai_providers.action_disable', { defaultValue: '停用' })
                : t('ai_providers.action_enable', { defaultValue: '启用' })}
            </Button>
          )}
          {onDelete && row && (
            <Button
              variant="danger"
              size="sm"
              onClick={() => onDelete(row)}
              disabled={actionsDisabled || deleteDisabled || editor.saving || rowDeletedExternally}
            >
              {t('common.delete')}
            </Button>
          )}
        </div>

        <div className={styles.drawerFooterRight}>
          {editor.isDirty || activeTab === 'config' || activeTab === 'models' ? (
            <>
              <Button
                variant="secondary"
                size="sm"
                onClick={handleResetForm}
                disabled={!editor.isDirty || editor.saving || actionsDisabled}
              >
                {t('common.reset', { defaultValue: '重置' })}
              </Button>
              <Button
                variant="primary"
                size="sm"
                onClick={() => void editor.handleSave()}
                loading={editor.saving}
                disabled={!editor.isDirty || !editor.canSave || actionsDisabled || rowDeletedExternally}
              >
                {t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })}
              </Button>
            </>
          ) : (
            <Button
              variant="primary"
              size="sm"
              onClick={() => switchTab('config')}
              disabled={actionsDisabled}
            >
              {t('ai_providers.drawer_edit_config_btn', { defaultValue: '编辑配置' })}
            </Button>
          )}
        </div>
      </div>
    );

  const tabItems: Array<{
    key: ProviderDrawerTab;
    label: string;
    count?: number;
    dirty?: boolean;
  }> =
    mode === 'view-edit'
      ? [
          {
            key: 'overview',
            label: t('ai_providers.drawer_tab_overview', { defaultValue: '概览' }),
          },
          {
            key: 'config',
            label: t('ai_providers.drawer_tab_config', { defaultValue: '配置' }),
            dirty: editor.isConfigDirty,
          },
          {
            key: 'models',
            label: t('ai_providers.drawer_tab_models', { defaultValue: '模型' }),
            count: editor.configuredModelCount,
            dirty: editor.isModelsDirty,
          },
          {
            key: 'connectivity',
            label: t('ai_providers.drawer_tab_connectivity', { defaultValue: '测活诊断' }),
          },
        ]
      : [
          {
            key: 'config',
            label: t('ai_providers.drawer_tab_create_config', { defaultValue: '基础配置' }),
            dirty: editor.isConfigDirty,
          },
          {
            key: 'models',
            label: t('ai_providers.drawer_tab_create_models', { defaultValue: '模型配置' }),
            count: editor.configuredModelCount,
            dirty: editor.isModelsDirty,
          },
          {
            key: 'connectivity',
            label: t('ai_providers.drawer_tab_create_connectivity', { defaultValue: '连通性测试' }),
          },
        ];

  return (
    <Drawer
      open={open}
      onClose={onClose}
      onBeforeClose={requestCloseOrSwitch}
      closeDisabled={editor.saving}
      width="clamp(560px, 50vw, 780px)"
      title={headerTitleNode}
      footer={footerNode}
      bodyRef={bodyRef}
    >
      <div className={styles.drawerBodyShell}>
        {mode === 'create' && availableCreateKinds.length > 1 && (
          <div className={styles.createKindBar}>
            <SegmentedTabs<ProviderKind>
              activeTab={effectiveCreateKind}
              onChange={(nextKind) => void handleSwitchCreateKind(nextKind)}
              items={createKindItems}
              size="sm"
              ariaLabel={t('ai_providers.create_kind_selector_label', {
                defaultValue: '选择提供商类型',
              })}
            />
          </div>
        )}

        <div
          className={styles.drawerTabs}
          role="tablist"
          aria-label={t('ai_providers.drawer_tabs_label', { defaultValue: '提供商配置标签页' })}
        >
          {tabItems.map((tab, index) => {
            const selected = activeTab === tab.key;
            const tabId = `${tabsIdPrefix}-tab-${tab.key}`;
            const panelId = `${tabsIdPrefix}-panel-${tab.key}`;
            return (
              <button
                key={tab.key}
                ref={(el) => {
                  tabRefs.current[tab.key] = el;
                }}
                id={tabId}
                type="button"
                role="tab"
                aria-selected={selected}
                aria-controls={panelId}
                tabIndex={selected ? 0 : -1}
                className={selected ? styles.drawerTabActive : undefined}
                onClick={() => switchTab(tab.key)}
                onKeyDown={(e) => handleTabKeyDown(e, index, tabItems)}
              >
                <span>{tab.label}</span>
                {tab.count !== undefined && (
                  <span className={styles.tabCountBadge}>{tab.count}</span>
                )}
                {tab.dirty && <span className={styles.tabDirtyDot} aria-hidden="true" />}
              </button>
            );
          })}
        </div>

        {rowDeletedExternally && (
          <div
            className={styles.drawerDisabledNotice}
            role="alert"
            data-testid="drawer-deleted-externally-notice"
          >
            <div className={styles.drawerDisabledText}>
              <strong>{t('notification.provider_conflict_refresh')}</strong>
              <p>
                {t('ai_providers.drawer_deleted_externally_desc', {
                  defaultValue: '该提供商配置已在外部被移除，当前未保存的草稿已保留；可复制内容后关闭抽屉。',
                })}
              </p>
            </div>
          </div>
        )}

        {editor.error && (
          <div className={styles.inlineError} role="alert">
            {editor.error}
          </div>
        )}

        <div
          role="tabpanel"
          id={`${tabsIdPrefix}-panel-${activeTab}`}
          aria-labelledby={`${tabsIdPrefix}-tab-${activeTab}`}
        >
          {activeTab === 'overview' && mode === 'view-edit' && row && (
            <ProviderOverviewTab
              row={row}
              form={editor.form}
              usageByProvider={usageByProvider}
              actionsDisabled={actionsDisabled}
              toggleDisabled={toggleDisabled || rowDeletedExternally}
              isDirty={editor.isDirty}
              saving={editor.saving}
              onSwitchTab={switchTab}
              onToggleEnabled={onToggleEnabled}
            />
          )}

          {activeTab === 'config' && (
            <ProviderConfigurationTab
              mode={mode}
              activeKind={activeKind}
              form={editor.form}
              setForm={editor.setForm}
              isDirty={editor.isDirty}
              canSave={editor.canSave}
              saving={editor.saving}
              actionsDisabled={actionsDisabled}
              v8AliasSupported={v8AliasSupported}
              onReset={handleResetForm}
              onSave={() => void editor.handleSave()}
              onOpenAIKeyCountChange={handleOpenAIKeyCountChange}
            />
          )}

          {activeTab === 'models' && (
            <ProviderModelsTab
              key={`${openEpoch}:${currentRowSessionToken}:${mode}:${activeKind}:${resetNonce}`}
              mode={mode}
              activeKind={activeKind}
              form={editor.form}
              setForm={editor.setForm}
              isDirty={editor.isDirty}
              canSave={editor.canSave}
              saving={editor.saving}
              actionsDisabled={actionsDisabled}
              configuredModelCount={editor.configuredModelCount}
              onReset={handleResetForm}
              onSave={() => void editor.handleSave()}
              modelDiscoveryOpen={editor.modelDiscoveryOpen}
              setModelDiscoveryOpen={editor.setModelDiscoveryOpen}
              modelDiscoveryEndpoint={editor.modelDiscoveryEndpoint}
              discoveredModels={editor.discoveredModels}
              modelsLoaded={editor.modelsLoaded}
              modelDiscoveryFetching={editor.modelDiscoveryFetching}
              modelDiscoveryError={editor.modelDiscoveryError}
              modelDiscoverySearch={editor.modelDiscoverySearch}
              setModelDiscoverySearch={editor.setModelDiscoverySearch}
              modelDiscoverySelected={editor.modelDiscoverySelected}
              discoveredModelsFiltered={editor.discoveredModelsFiltered}
              configuredModelNames={editor.configuredModelNames}
              visibleDiscoverableModelNames={editor.visibleDiscoverableModelNames}
              allVisibleSelected={editor.allVisibleSelected}
              toggleModelDiscoverySelection={editor.toggleModelDiscoverySelection}
              handleSelectVisibleModels={editor.handleSelectVisibleModels}
              handleClearModelDiscoverySelection={editor.handleClearModelDiscoverySelection}
              fetchModelDiscovery={editor.fetchModelDiscovery}
              applySelectedDiscoveredModels={editor.applySelectedDiscoveredModels}
            />
          )}

          {activeTab === 'connectivity' && (
            <ProviderConnectivityTab
              activeKind={activeKind}
              form={editor.form}
              actionsDisabled={actionsDisabled}
              saving={editor.saving}
              isTesting={editor.isTesting}
              connectivityTestModel={editor.connectivityTestModel}
              onConnectivityTestModelChange={editor.setConnectivityTestModel}
              modelSelectOptions={editor.modelSelectOptions}
              singleTestResult={editor.singleTestResult}
              openAIAllTestResult={editor.openAIAllTestResult}
              openAIKeyStatuses={editor.openAIKeyStatuses}
              onRunSingleTest={editor.runSingleConnectivityTest}
              onTestSingleOpenAIKey={editor.testSingleOpenAIKey}
              onTestAllOpenAIKeys={editor.testAllOpenAIKeys}
            />
          )}
        </div>
      </div>
    </Drawer>
  );
}
