import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  DRAWER_WIDTH,
  DrawerActionFooter,
  DrawerBodyShell,
  DrawerIdentityHeader,
  DrawerNotice,
  DrawerStatusPill,
  DrawerTabPanel,
  DrawerViewport,
} from '@/components/common/WorkspaceDrawer';
import { Button } from '@/components/ui/Button';
import { Drawer } from '@/components/ui/Drawer';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { IconCheck, IconRefreshCw, IconShield, IconX } from '@/components/ui/icons';
import type { ProviderRow } from '../ProviderTable/rowData';
import {
  buildProviderHealthCheckItems,
  getProviderHealthCheckApplyActions,
  runProviderHealthCheckItem,
  summarizeProviderHealthCheckItems,
  type ProviderHealthCheckApplyAction,
  type ProviderHealthCheckItem,
} from './healthCheck';
import styles from './ProviderHealthCheckDrawer.module.scss';

interface ProviderHealthCheckGroup {
  providerKey: string;
  providerLabel: string;
  providerSubtitle: string;
  items: ProviderHealthCheckItem[];
}

export interface ProviderHealthCheckDrawerProps {
  open: boolean;
  rows: ProviderRow[];
  allRows?: ProviderRow[];
  actionsDisabled: boolean;
  scopeMode?: 'filtered' | 'selected' | 'single';
  onOpenProviderConfig?: (row: ProviderRow) => void | Promise<void>;
  onClose: () => void;
  onApplyResultActions: (actions: Map<string, ProviderHealthCheckApplyAction>) => Promise<void>;
  onSetProviderEnabled: (providerKey: string, enabled: boolean) => Promise<void>;
}

const statusClassName = (status: ProviderHealthCheckItem['status']) =>
  [
    styles.statusBadge,
    status === 'success'
      ? styles.statusSuccess
      : status === 'error'
        ? styles.statusError
        : status === 'running'
          ? styles.statusRunning
          : styles.statusPending,
  ].join(' ');

const buildHealthCheckGroups = (items: ProviderHealthCheckItem[]): ProviderHealthCheckGroup[] => {
  const groups: ProviderHealthCheckGroup[] = [];
  const groupByKey = new Map<string, ProviderHealthCheckGroup>();

  items.forEach((item) => {
    const existing = groupByKey.get(item.providerKey);
    if (existing) {
      existing.items.push(item);
      return;
    }

    const group = {
      providerKey: item.providerKey,
      providerLabel: item.providerLabel,
      providerSubtitle: item.providerSubtitle,
      items: [item],
    };
    groupByKey.set(item.providerKey, group);
    groups.push(group);
  });

  return groups;
};

export function ProviderHealthCheckDrawer({
  open,
  rows,
  allRows,
  actionsDisabled,
  scopeMode,
  onOpenProviderConfig,
  onClose,
  onApplyResultActions,
  onSetProviderEnabled,
}: ProviderHealthCheckDrawerProps) {
  const { t } = useTranslation();
  const rowsRef = useRef(rows);
  const prevOpenRef = useRef(false);
  const runIdRef = useRef(0);
  const [snapshotRows, setSnapshotRows] = useState<ProviderRow[]>(() => (open ? rows : []));
  const [items, setItems] = useState<ProviderHealthCheckItem[]>([]);
  const [running, setRunning] = useState(false);
  const [applying, setApplying] = useState(false);
  const [rerunIndex, setRerunIndex] = useState(0);
  const [manualSwitchingKey, setManualSwitchingKey] = useState<string | null>(null);

  useEffect(() => {
    rowsRef.current = rows;
  }, [rows]);

  const liveRowByKey = useMemo(() => {
    const map = new Map<string, ProviderRow>();
    allRows?.forEach((row) => map.set(row.key, row));
    rows.forEach((row) => map.set(row.key, row));
    return map;
  }, [allRows, rows]);

  const effectiveRows = useMemo(() => {
    const base = snapshotRows.length > 0 ? snapshotRows : rows;
    return base.map((row) => liveRowByKey.get(row.key) ?? row);
  }, [liveRowByKey, rows, snapshotRows]);

  const effectiveRowsRef = useRef(effectiveRows);
  effectiveRowsRef.current = effectiveRows;

  useEffect(() => {
    if (!open) {
      prevOpenRef.current = false;
      runIdRef.current += 1;
      setRunning(false);
      setApplying(false);
      setManualSwitchingKey(null);
      setSnapshotRows([]);
      setRerunIndex(0);
      return;
    }

    const isFreshOpen = !prevOpenRef.current;
    prevOpenRef.current = true;
    const runRows = isFreshOpen || rerunIndex === 0 ? rowsRef.current : effectiveRowsRef.current;
    if (isFreshOpen) {
      setSnapshotRows(rowsRef.current);
    }

    const initialItems = buildProviderHealthCheckItems(runRows);
    const runId = runIdRef.current + 1;
    runIdRef.current = runId;
    setItems(initialItems);
    setRunning(initialItems.length > 0);
    setApplying(false);
    setManualSwitchingKey(null);

    if (initialItems.length === 0) return;

    void (async () => {
      for (const item of initialItems) {
        if (runIdRef.current !== runId) return;
        setItems((prev) =>
          prev.map((entry) =>
            entry.id === item.id
              ? { ...entry, status: 'running', message: t('ai_providers.health_check_running') }
              : entry
          )
        );

        const result = await runProviderHealthCheckItem(runRows, item);
        if (runIdRef.current !== runId) return;
        setItems((prev) => prev.map((entry) => (entry.id === item.id ? result : entry)));
      }

      if (runIdRef.current === runId) {
        setRunning(false);
      }
    })();

    return () => {
      runIdRef.current += 1;
    };
  }, [open, rerunIndex, t]);

  const summary = useMemo(() => summarizeProviderHealthCheckItems(items), [items]);
  const complete = summary.total > 0 && summary.completed === summary.total && !running;
  const resultActions = useMemo(() => getProviderHealthCheckApplyActions(items), [items]);
  const groups = useMemo(() => buildHealthCheckGroups(items), [items]);
  const rowByKey = useMemo(
    () => new Map(effectiveRows.map((row) => [row.key, row])),
    [effectiveRows]
  );
  const providerEnabledByKey = useMemo(() => {
    const map = new Map<string, boolean>();
    effectiveRows.forEach((row) => {
      map.set(row.key, row.enabled);
    });
    return map;
  }, [effectiveRows]);
  const renderLocalizedText = (
    fallback: string,
    key?: string,
    values?: Record<string, string | number>
  ) => (key ? t(key, { defaultValue: fallback, ...(values ?? {}) }) : fallback);

  const handleApplyResults = async () => {
    if (!complete || resultActions.size === 0 || applying) return;
    setApplying(true);
    try {
      await onApplyResultActions(resultActions);
      setSnapshotRows((prev) =>
        prev.map((row) => {
          const action = resultActions.get(row.key);
          return action ? { ...row, enabled: action === 'enable' } : row;
        })
      );
    } finally {
      setApplying(false);
    }
  };

  const handleSetAll = async (enabled: boolean) => {
    if (applying) return;
    const uniqueKeys = Array.from(new Set(items.map((item) => item.providerKey)));
    setApplying(true);
    try {
      const actions = new Map<string, ProviderHealthCheckApplyAction>();
      uniqueKeys.forEach((providerKey) => {
        actions.set(providerKey, enabled ? 'enable' : 'disable');
      });
      await onApplyResultActions(actions);
      setSnapshotRows((prev) =>
        prev.map((row) => (actions.has(row.key) ? { ...row, enabled } : row))
      );
    } finally {
      setApplying(false);
    }
  };

  const handleManualToggle = async (providerKey: string, enabled: boolean) => {
    if (manualSwitchingKey || applying) return;
    setManualSwitchingKey(providerKey);
    try {
      await onSetProviderEnabled(providerKey, enabled);
      setSnapshotRows((prev) =>
        prev.map((row) => (row.key === providerKey ? { ...row, enabled } : row))
      );
    } finally {
      setManualSwitchingKey(null);
    }
  };

  const scopeBadgeLabel =
    scopeMode === 'single'
      ? t('ai_providers.health_check_scope_single_badge', { defaultValue: '单渠道测活' })
      : scopeMode === 'selected'
        ? t('ai_providers.health_check_scope_selected_badge', { defaultValue: '已选范围' })
        : t('ai_providers.health_check_scope_batch_badge', { defaultValue: '批量巡检' });

  const scopeDescText =
    scopeMode === 'single' && effectiveRows[0]
      ? t('ai_providers.health_check_scope_single_desc', {
          defaultValue: `正在检测「${effectiveRows[0].label}」的连通性与可用模型`,
          name: effectiveRows[0].label,
        })
      : scopeMode === 'selected'
        ? t('ai_providers.health_check_scope_selected_desc', {
            defaultValue: '当前仅对已勾选的 {{count}} 个提供商执行 /models 健康巡检',
            count: effectiveRows.length,
          })
        : t('ai_providers.health_check_scope_batch_desc', {
            defaultValue:
              '通过 /models 端点快速探测 {{count}} 个渠道连通性，完成后可按结果一键启停',
            count: effectiveRows.length,
          });

  const footer = (
    <DrawerActionFooter
      left={
        <Button
          variant="secondary"
          size="sm"
          onClick={() => setRerunIndex((value) => value + 1)}
          disabled={actionsDisabled || running || applying}
        >
          <IconRefreshCw size={14} />
          {t('ai_providers.health_check_rerun')}
        </Button>
      }
      right={
        <>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => void handleSetAll(true)}
            disabled={actionsDisabled || running || applying || items.length === 0}
          >
            {t('ai_providers.health_check_enable_all')}
          </Button>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => void handleSetAll(false)}
            disabled={actionsDisabled || running || applying || items.length === 0}
          >
            {t('ai_providers.health_check_disable_all')}
          </Button>
          <Button
            size="sm"
            onClick={() => void handleApplyResults()}
            loading={applying}
            disabled={
              actionsDisabled || running || applying || !complete || resultActions.size === 0
            }
          >
            {t('ai_providers.health_check_apply_results')}
          </Button>
        </>
      }
    />
  );

  return (
    <Drawer
      open={open}
      onClose={onClose}
      closeDisabled={running || applying}
      width={DRAWER_WIDTH.MD}
      footer={footer}
      title={
        <DrawerIdentityHeader
          icon={<IconShield size={18} />}
          iconBoxed
          title={t('ai_providers.health_check_title')}
          metaItems={[
            <DrawerStatusPill key="scope" tone={running ? 'info' : 'neutral'}>
              {scopeBadgeLabel}
            </DrawerStatusPill>,
            <span key="summary">
              {t('ai_providers.health_check_progress_status', {
                completed: summary.completed,
                total: summary.total,
              })}
            </span>,
          ]}
        />
      }
    >
      <DrawerBodyShell>
        <DrawerViewport>
          <DrawerTabPanel className={styles.root}>
            <DrawerNotice tone="info" role="status" title={scopeBadgeLabel}>
              {scopeDescText}
            </DrawerNotice>
        <section className={styles.progressSection}>
          <div className={styles.progressHeader}>
            <div>
              <strong>{t('ai_providers.health_check_progress_title')}</strong>
              <span>
                {t('ai_providers.health_check_progress_status', {
                  completed: summary.completed,
                  total: summary.total,
                })}
              </span>
            </div>
            <div>
              <span>
                {t('ai_providers.health_check_progress_counts', {
                  success: summary.success,
                  failed: summary.error,
                })}
              </span>
              <strong>{`${summary.percent}%`}</strong>
            </div>
          </div>
          <div className={styles.progressTrack}>
            <span
              className={styles.progressBar}
              style={{ width: `${Math.max(0, Math.min(100, summary.percent))}%` }}
            />
          </div>
        </section>

        {items.length === 0 ? (
          <div className={styles.empty}>{t('ai_providers.health_check_empty')}</div>
        ) : (
          <div className={styles.resultList}>
            {groups.map((group) => {
              const groupSummary = summarizeProviderHealthCheckItems(group.items);
              const enabled = providerEnabledByKey.get(group.providerKey) ?? false;
              const switching = manualSwitchingKey === group.providerKey || applying;
              const groupRow = rowByKey.get(group.providerKey);
              return (
                <section key={group.providerKey} className={styles.resultGroup}>
                  <div className={styles.groupHeader}>
                    <div className={styles.groupTitle}>
                      <div className={styles.groupIdentity}>
                        <strong>{group.providerLabel}</strong>
                        {groupRow?.alias && groupRow.maskedKey && (
                          <span className={styles.groupMetaCode}>{groupRow.maskedKey}</span>
                        )}
                        {groupRow?.alias && groupRow.hostLabel ? (
                          <span
                            className={styles.groupMetaHost}
                            title={group.providerSubtitle || undefined}
                          >
                            {groupRow.hostLabel}
                          </span>
                        ) : (
                          group.providerSubtitle && (
                            <span title={group.providerSubtitle}>{group.providerSubtitle}</span>
                          )
                        )}
                      </div>
                      <div className={styles.groupSummary}>
                        <span>
                          {groupSummary.success}
                          {' '}
                          {t('ai_providers.health_check_status_success')}
                        </span>
                        {groupSummary.error > 0 && (
                          <span className={styles.groupFailureCount}>
                            {groupSummary.error}
                            {' '}
                            {t('ai_providers.health_check_status_error')}
                          </span>
                        )}
                      </div>
                    </div>
                    <div className={styles.groupControls}>
                      {onOpenProviderConfig && groupRow && (
                        <Button
                          variant="secondary"
                          size="xs"
                          onClick={() => void onOpenProviderConfig(groupRow)}
                          disabled={actionsDisabled || applying}
                        >
                          {t('ai_providers.health_check_open_config', { defaultValue: '配置' })}
                        </Button>
                      )}
                      <ToggleSwitch
                        checked={enabled}
                        disabled={actionsDisabled || running || switching}
                        onChange={(value) => void handleManualToggle(group.providerKey, value)}
                        ariaLabel={t('ai_providers.health_check_manual_toggle')}
                      />
                    </div>
                  </div>

                  <div className={styles.groupRows}>
                    {group.items.map((item) => {
                      const targetLabel = renderLocalizedText(
                        item.targetLabel,
                        item.targetLabelKey,
                        item.targetLabelValues
                      );
                      const detailLabel = renderLocalizedText(
                        item.detailLabel,
                        item.detailLabelKey,
                        item.detailLabelValues
                      );
                      const message = renderLocalizedText(
                        item.message,
                        item.messageKey,
                        item.messageValues
                      );
                      return (
                        <div key={item.id} className={styles.resultRow} title={detailLabel}>
                          <div className={styles.resultStatus} aria-hidden="true">
                            {item.status === 'success' ? (
                              <IconCheck size={14} />
                            ) : item.status === 'error' ? (
                              <IconX size={14} />
                            ) : item.status === 'running' ? (
                              <span className={styles.spinner} />
                            ) : (
                              <span className={styles.pendingDot} />
                            )}
                          </div>

                          <div className={styles.resultMain}>
                            <div className={styles.resultTitleLine}>
                              <span className={styles.targetLabel}>{targetLabel}</span>
                              <span className={statusClassName(item.status)}>
                                {t(`ai_providers.health_check_status_${item.status}`)}
                              </span>
                              {item.status === 'success' && (
                                <span className={styles.resultMetric}>
                                  {t('ai_providers.health_check_models_count', {
                                    count: item.modelCount ?? 0,
                                  })}
                                </span>
                              )}
                              {item.status === 'error' && message && (
                                <span className={styles.resultReason}>{message}</span>
                              )}
                              {item.status !== 'success' && item.status !== 'error' && message && (
                                <span className={styles.resultMessage}>{message}</span>
                              )}
                              {item.durationMs !== undefined && (
                                <span className={styles.resultMetric}>
                                  {t('ai_providers.health_check_duration', {
                                    ms: item.durationMs,
                                  })}
                                </span>
                              )}
                            </div>
                          </div>
                        </div>
                      );
                    })}
                  </div>
                </section>
              );
            })}
          </div>
        )}
          </DrawerTabPanel>
        </DrawerViewport>
      </DrawerBodyShell>
    </Drawer>
  );
}
