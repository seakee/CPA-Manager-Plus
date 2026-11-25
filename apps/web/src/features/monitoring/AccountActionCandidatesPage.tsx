import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { PageControlBar } from '@/components/common/PageControlBar';
import { LocalizedPaginationFooter } from '@/components/common/LocalizedPaginationFooter';
import { WorkspaceListPanel } from '@/components/common/WorkspaceListPanel';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { SegmentedTabs, type SegmentedTabItem } from '@/components/ui/SegmentedTabs';
import {
  IconCheck,
  IconCopy,
  IconEye,
  IconRefreshCw,
  IconSearch,
  IconTrash2,
  IconX,
} from '@/components/ui/icons';
import { useClientPagination } from '@/hooks/list';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import {
  usageServiceApi,
  type AccountActionCandidate,
  type AccountActionStatus,
} from '@/services/api/usageService';
import { useAuthStore, useNotificationStore } from '@/stores';
import { formatDateTime, maskSensitiveText } from '@/utils/format';
import styles from './AccountActionCandidatesPage.module.scss';

type StatusFilter = 'pending' | 'all' | 'ignored' | 'resolved' | 'deleted';

type CandidateAction = 'ignore' | 'resolve' | 'enable' | 'delete';

interface AccountActionEvidenceFields {
  headerErrorKind?: unknown;
  headerErrorCode?: unknown;
  headerTraceId?: unknown;
  statusCode?: unknown;
  model?: unknown;
  endpoint?: unknown;
}

const STATUS_FILTERS: StatusFilter[] = ['pending', 'all', 'ignored', 'resolved', 'deleted'];
const DEFAULT_PAGE_SIZE = 20;
const PAGE_SIZE_OPTIONS = [20, 50, 100] as const;

const formatMs = (value?: number) => {
  if (!value) return '-';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '-';
  return formatDateTime(date);
};

const stringifyEvidence = (candidate: AccountActionCandidate | null) => {
  if (!candidate?.evidence) return '';
  try {
    return maskSensitiveText(JSON.stringify(candidate.evidence, null, 2));
  } catch {
    return maskSensitiveText(String(candidate.evidence));
  }
};

const readEvidenceString = (
  candidate: AccountActionCandidate | null,
  key: keyof AccountActionEvidenceFields
) => {
  if (!candidate) return '';
  const evidence = candidate.evidence;
  if (!evidence || typeof evidence !== 'object' || Array.isArray(evidence)) return '';
  const value = (evidence as AccountActionEvidenceFields)[key];
  if (typeof value === 'string') return value.trim();
  if (typeof value === 'number' || typeof value === 'boolean') return String(value);
  return '';
};

const getHeaderEvidence = (candidate: AccountActionCandidate | null) => ({
  errorKind: readEvidenceString(candidate, 'headerErrorKind'),
  errorCode: readEvidenceString(candidate, 'headerErrorCode'),
  traceId: readEvidenceString(candidate, 'headerTraceId'),
  statusCode: readEvidenceString(candidate, 'statusCode'),
  model: readEvidenceString(candidate, 'model'),
  endpoint: readEvidenceString(candidate, 'endpoint'),
});

export function AccountActionCandidatesPage() {
  const connectionScope = useAuthStore((state) =>
    JSON.stringify([state.apiBase, state.managementKey])
  );
  const { managerServiceBase } = usePanelFeatureAvailability();
  return <AccountActionCandidatesPageContent key={`${connectionScope}:${managerServiceBase}`} />;
}

function AccountActionCandidatesPageContent() {
  const { t } = useTranslation();
  const managementKey = useAuthStore((state) => state.managementKey);
  const { showNotification, showConfirmation } = useNotificationStore();
  const featureAvailability = usePanelFeatureAvailability();
  const [items, setItems] = useState<AccountActionCandidate[]>([]);
  const [pendingCount, setPendingCount] = useState(0);
  const [filter, setFilter] = useState<StatusFilter>('pending');
  const [search, setSearch] = useState('');
  const [loading, setLoading] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState('');
  const [actingId, setActingId] = useState<number | null>(null);
  const [evidenceCandidate, setEvidenceCandidate] = useState<AccountActionCandidate | null>(null);

  const managerBase = featureAvailability.managerServiceBase;
  const requestIdRef = useRef(0);
  const scopeActiveRef = useRef(false);
  useLayoutEffect(() => {
    scopeActiveRef.current = true;
    return () => {
      scopeActiveRef.current = false;
    };
  }, []);
  useLayoutEffect(() => {
    requestIdRef.current += 1;
    setItems([]);
    setLoaded(false);
    setPendingCount(0);
    setError('');
    setLoading(true);
    return () => {
      requestIdRef.current += 1;
    };
  }, [managerBase, managementKey, filter]);

  const loadCandidates = useCallback(async () => {
    const requestId = ++requestIdRef.current;
    if (!managerBase || !managementKey) {
      setLoading(false);
      return;
    }
    setLoading(true);
    setError('');
    try {
      const response = await usageServiceApi.listAccountActionCandidates(
        managerBase,
        managementKey,
        filter === 'all' ? '' : filter,
        200
      );
      if (requestId !== requestIdRef.current) return;
      setItems(response.items);
      setLoaded(true);
      setPendingCount(response.pendingCount);
    } catch (err) {
      if (requestId !== requestIdRef.current) return;
      const message = err instanceof Error ? err.message : String(err || 'request failed');
      setError(message);
      showNotification(
        t('account_actions.load_failed', { message, defaultValue: `Load failed: ${message}` }),
        'error'
      );
    } finally {
      if (requestId === requestIdRef.current) setLoading(false);
    }
  }, [filter, managementKey, managerBase, showNotification, t]);

  const loadCandidatesRef = useRef(loadCandidates);
  loadCandidatesRef.current = loadCandidates;

  useHeaderRefresh(loadCandidates, Boolean(managerBase && managementKey));

  useEffect(() => {
    if (featureAvailability.checking) return;
    void loadCandidates();
  }, [featureAvailability.checking, loadCandidates]);

  const statusCounts = useMemo(() => {
    const counts: Partial<Record<StatusFilter, number>> = { pending: pendingCount };
    if (filter !== 'pending') {
      const bucket: Record<string, number> = {};
      for (const item of items) {
        bucket[item.status] = (bucket[item.status] || 0) + 1;
      }
      if (filter === 'all') {
        counts.ignored = bucket.ignored ?? 0;
        counts.resolved = bucket.resolved ?? 0;
        counts.deleted = bucket.deleted ?? 0;
      } else {
        counts[filter] = items.length;
      }
    }
    return counts;
  }, [filter, items, pendingCount]);

  const visibleItems = useMemo(() => {
    const term = search.trim().toLowerCase();
    if (!term) return items;
    return items.filter((item) => {
      const header = getHeaderEvidence(item);
      return [
        item.accountSnapshot,
        item.authLabel,
        item.accountIdSnapshot,
        item.provider,
        item.authFileName,
        item.authIndex,
        item.actionType,
        item.reason,
        item.reasonCode,
        item.status,
        item.lastError,
        header.errorKind,
        header.errorCode,
        header.traceId,
        header.model,
      ]
        .filter(Boolean)
        .some((value) => String(value).toLowerCase().includes(term));
    });
  }, [items, search]);

  const {
    page: currentPage,
    setPage,
    pageSize,
    setPageSize,
    totalPages,
    startItem: paginationStartItem,
    endItem: paginationEndItem,
    pageItems: paginatedItems,
  } = useClientPagination(visibleItems, {
    initialPageSize: DEFAULT_PAGE_SIZE,
    resetDeps: [managerBase, managementKey, filter, search],
  });

  const filterTabItems = useMemo<ReadonlyArray<SegmentedTabItem<StatusFilter>>>(
    () =>
      STATUS_FILTERS.map((key) => {
        if (key === 'all') {
          return {
            id: key,
            label: t(`account_actions.filter_${key}`),
          };
        }
        const count = loaded ? statusCounts[key] : undefined;
        return {
          id: key,
          label: (
            <span className={styles.filterTabLabel}>
              <span>{t(`account_actions.filter_${key}`)}</span>
              {typeof count === 'number' ? (
                <strong className={styles.filterCount}>{count}</strong>
              ) : null}
            </span>
          ),
        };
      }),
    [loaded, statusCounts, t]
  );

  const runAction = useCallback(
    async (candidate: AccountActionCandidate, action: CandidateAction) => {
      if (!scopeActiveRef.current || !managerBase || !managementKey) return;
      setActingId(candidate.id);
      try {
        switch (action) {
          case 'ignore':
            await usageServiceApi.ignoreAccountActionCandidate(managerBase, managementKey, candidate.id);
            break;
          case 'resolve':
            await usageServiceApi.resolveAccountActionCandidate(managerBase, managementKey, candidate.id);
            break;
          case 'enable':
            await usageServiceApi.enableAccountActionCandidate(managerBase, managementKey, candidate.id);
            break;
          case 'delete':
            await usageServiceApi.deleteAccountActionCandidateAuthFile(
              managerBase,
              managementKey,
              candidate.id
            );
            break;
        }
        if (!scopeActiveRef.current) return;
        showNotification(t(`account_actions.${action}_success`), 'success');
        await loadCandidatesRef.current();
      } catch (err) {
        if (!scopeActiveRef.current) return;
        const message = err instanceof Error ? err.message : String(err || 'request failed');
        showNotification(
          t('account_actions.action_failed', {
            message,
            defaultValue: `Action failed: ${message}`,
          }),
          'error'
        );
      } finally {
        setActingId(null);
      }
    },
    [managementKey, managerBase, showNotification, t]
  );

  const confirmDelete = useCallback(
    (candidate: AccountActionCandidate) => {
      showConfirmation({
        title: t('account_actions.confirm_delete_title'),
        message: (
          <span>
            {t('account_actions.confirm_delete_message', {
              file: candidate.authFileName,
            })}
          </span>
        ),
        confirmText: t('account_actions.confirm_delete_button'),
        cancelText: t('common.cancel'),
        variant: 'danger',
        onConfirm: () => runAction(candidate, 'delete'),
      });
    },
    [runAction, showConfirmation, t]
  );

  const actionLabel = (actionType: string) =>
    t(`account_actions.action_type_${actionType}`, {
      defaultValue: actionType || '-',
    });

  const statusLabel = (status: AccountActionStatus) =>
    t(`account_actions.status_${status}`, {
      defaultValue: status || '-',
    });

  const statusPillToneClass = error
    ? styles.statusPillDanger
    : !loaded && loading
      ? styles.statusPillWarning
      : pendingCount > 0
        ? styles.statusPillWarning
        : styles.statusPillSuccess;

  const statusPillText = error
    ? t('account_actions.load_failed_title')
    : !loaded && loading
      ? t('common.loading')
      : pendingCount > 0
        ? t('account_actions.status_pending_pill', {
            count: pendingCount,
            defaultValue: `Pending: ${pendingCount}`,
          })
        : t('account_actions.status_clear_pill', {
            defaultValue: 'No pending issues',
          });

  const evidenceText = stringifyEvidence(evidenceCandidate);
  const evidenceHeader = getHeaderEvidence(evidenceCandidate);

  const handleCopyEvidence = useCallback(async () => {
    if (!evidenceText) return;
    try {
      await navigator.clipboard?.writeText(evidenceText);
      showNotification(
        t('account_actions.evidence_copied', { defaultValue: 'Evidence copied' }),
        'success'
      );
    } catch {
      // Clipboard API unavailable in restricted contexts
    }
  }, [evidenceText, showNotification, t]);

  const searchPlaceholder = t('account_actions.search_placeholder', {
    defaultValue: 'Search account, file, reason, or error message',
  });

  return (
    <div className={styles.page}>
      <PageControlBar
        ariaLabel={t('account_actions.title')}
        leading={
          <>
            <span className={`${styles.statusPill} ${statusPillToneClass}`}>
              <span className={styles.statusDot} aria-hidden="true" />
              <span>{statusPillText}</span>
            </span>
            {loaded && visibleItems.length > 0 ? (
              <span className={styles.metaPill}>
                {t('account_actions.visible_badge', {
                  count: visibleItems.length,
                  defaultValue: `Showing: ${visibleItems.length}`,
                })}
              </span>
            ) : null}
          </>
        }
        actions={
          <>
            {featureAvailability.requestMonitoringAvailable ? (
              <Link to="/monitoring" className="btn btn-secondary btn-sm">
                {t('account_actions.back_to_monitoring')}
              </Link>
            ) : null}
            <Button variant="secondary" size="sm" onClick={loadCandidates} loading={loading}>
              <IconRefreshCw size={15} />
              {t('common.refresh')}
            </Button>
          </>
        }
        toolbar={
          <div className={styles.toolbarRow}>
            <SegmentedTabs<StatusFilter>
              items={filterTabItems}
              activeTab={filter}
              onChange={(nextFilter) => {
                setFilter(nextFilter);
                setPage(1);
              }}
              ariaLabel={t('account_actions.status_filter')}
              idBase="account-actions-filter"
              variant="joined"
              responsiveFullWidth={false}
              className={styles.filterTabs}
            />
            <div className={styles.searchWrap}>
              <Input
                className={styles.searchInput}
                value={search}
                onChange={(event) => {
                  setSearch(event.target.value);
                  setPage(1);
                }}
                placeholder={searchPlaceholder}
                aria-label={searchPlaceholder}
                rightElement={
                  search ? (
                    <button
                      type="button"
                      className={styles.searchClear}
                      onClick={() => {
                        setSearch('');
                        setPage(1);
                      }}
                      aria-label={t('common.clear', { defaultValue: 'Clear' })}
                      title={t('common.clear', { defaultValue: 'Clear' })}
                    >
                      <IconX size={15} />
                    </button>
                  ) : (
                    <IconSearch size={15} className={styles.searchIcon} />
                  )
                }
              />
            </div>
          </div>
        }
      />

      <WorkspaceListPanel
        summary={t('account_actions.list_summary', {
          count: visibleItems.length,
          defaultValue: `共 ${visibleItems.length} 条记录`,
        })}
        banner={
          error ? (
            <div className={styles.errorBanner} role="alert">
              <div className={styles.errorBannerCopy}>
                <strong>{t('account_actions.load_failed_title')}</strong>
                <span>{error}</span>
              </div>
              <Button variant="secondary" size="sm" onClick={loadCandidates} loading={loading}>
                <IconRefreshCw size={14} />
                {t('common.refresh')}
              </Button>
            </div>
          ) : null
        }
        empty={visibleItems.length === 0 && loaded}
        emptyFallback={
          <EmptyState
            variant="canvas"
            title={
              search.trim()
                ? t('account_actions.empty_filtered_title', {
                    defaultValue: 'No matching auth issues',
                  })
                : t('account_actions.empty_title')
            }
            description={
              search.trim()
                ? t('account_actions.empty_filtered_desc', {
                    query: search.trim(),
                    defaultValue: `No issues matched "${search.trim()}". Clear the search or switch status filter.`,
                  })
                : t('account_actions.empty_desc')
            }
            action={
              search.trim() ? (
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => {
                    setSearch('');
                    setPage(1);
                  }}
                >
                  {t('account_actions.reset_filters', { defaultValue: 'Clear search' })}
                </Button>
              ) : (
                <Button variant="secondary" size="sm" onClick={loadCandidates} loading={loading}>
                  <IconRefreshCw size={15} />
                  {t('common.refresh')}
                </Button>
              )
            }
          />
        }
        footer={
          <LocalizedPaginationFooter
            variant="flush"
            currentPage={currentPage}
            totalPages={totalPages}
            totalCount={visibleItems.length}
            startItem={paginationStartItem}
            endItem={paginationEndItem}
            disabled={!loaded || visibleItems.length === 0}
            pageSize={pageSize}
            pageSizeOptions={PAGE_SIZE_OPTIONS}
            onPageChange={setPage}
            onPageSizeChange={setPageSize}
          />
        }
      >
        {visibleItems.length > 0 ? (
          <div className={styles.tableWrap}>
            <table className={styles.table}>
              <thead>
                <tr>
                  <th>{t('account_actions.col_account')}</th>
                  <th>{t('account_actions.col_action')}</th>
                  <th>{t('account_actions.col_reason')}</th>
                  <th>{t('account_actions.col_seen')}</th>
                  <th>{t('account_actions.col_status')}</th>
                  <th className={styles.actionsCol}>{t('account_actions.col_operations')}</th>
                </tr>
              </thead>
              <tbody>
                {paginatedItems.map((candidate) => {
                  const busy = actingId === candidate.id;
                  const accountDisplay = candidate.accountSnapshot || candidate.authLabel || '-';
                  const reasonText = candidate.reasonCode
                    ? t(`account_actions.reason_${candidate.reasonCode}`, {
                        defaultValue: candidate.reason || '-',
                      })
                    : candidate.reason || '-';
                  const identityTitle = [
                    candidate.authFileName,
                    candidate.accountIdSnapshot,
                    candidate.authIndex,
                  ]
                    .filter(Boolean)
                    .join(' · ');
                  return (
                    <tr key={candidate.id}>
                      <td data-label={t('account_actions.col_account')}>
                        <div className={styles.accountCell}>
                          <div className={styles.accountPrimary}>
                            <strong title={accountDisplay}>{accountDisplay}</strong>
                            {candidate.provider ? (
                              <span className={styles.providerTag}>{candidate.provider}</span>
                            ) : null}
                          </div>
                          <code className={styles.fileName} title={identityTitle}>
                            {candidate.authFileName}
                          </code>
                        </div>
                      </td>
                      <td data-label={t('account_actions.col_action')}>
                        <div className={styles.badgeStack}>
                          <span
                            className={[
                              styles.actionBadge,
                              candidate.actionType === 'delete' ? styles.actionDelete : '',
                              candidate.actionType === 'reauth' ? styles.actionReauth : '',
                            ]
                              .filter(Boolean)
                              .join(' ')}
                          >
                            {actionLabel(candidate.actionType)}
                          </span>
                          {candidate.autoDisabledAtMs ? (
                            <span className={styles.autoDisabledText}>
                              {t('account_actions.auto_disabled_badge', {
                                defaultValue: 'Auto-disabled',
                              })}
                            </span>
                          ) : null}
                        </div>
                      </td>
                      <td data-label={t('account_actions.col_reason')}>
                        <div className={styles.reasonCell}>
                          <span className={styles.reasonTitle}>{reasonText}</span>
                          {candidate.lastError ? (
                            <small className={styles.errorText}>{candidate.lastError}</small>
                          ) : null}
                        </div>
                      </td>
                      <td data-label={t('account_actions.col_seen')}>
                        <div className={styles.timeCell}>
                          <span>{formatMs(candidate.lastSeenAtMs)}</span>
                          <small>
                            {t('account_actions.hits_count', {
                              count: candidate.hitCount,
                              defaultValue: `${candidate.hitCount} hits`,
                            })}
                          </small>
                        </div>
                      </td>
                      <td data-label={t('account_actions.col_status')}>
                        <span
                          className={[
                            styles.statusBadge,
                            candidate.status === 'pending' ? styles.statusPending : '',
                            candidate.status === 'ignored' ? styles.statusIgnored : '',
                            candidate.status === 'resolved' ? styles.statusResolved : '',
                            candidate.status === 'deleted' ? styles.statusDeleted : '',
                          ]
                            .filter(Boolean)
                            .join(' ')}
                        >
                          <span className={styles.statusDot} aria-hidden="true" />
                          <span>{statusLabel(candidate.status)}</span>
                        </span>
                      </td>
                      <td
                        data-label={t('account_actions.col_operations')}
                        className={styles.actionsCol}
                      >
                        <div className={styles.rowActions}>
                          <Button
                            variant="ghost"
                            size="xs"
                            onClick={() => setEvidenceCandidate(candidate)}
                            disabled={busy}
                          >
                            <IconEye size={14} />
                            {t('account_actions.view_evidence')}
                          </Button>
                          {candidate.status === 'pending' && (
                            <>
                              <Button
                                variant="secondary"
                                size="xs"
                                onClick={() => runAction(candidate, 'enable')}
                                loading={busy}
                              >
                                <IconCheck size={14} />
                                {t('account_actions.enable')}
                              </Button>
                              <Button
                                variant="secondary"
                                size="xs"
                                onClick={() => runAction(candidate, 'resolve')}
                                disabled={busy}
                              >
                                {t('account_actions.resolve')}
                              </Button>
                              <Button
                                variant="ghost"
                                size="xs"
                                onClick={() => runAction(candidate, 'ignore')}
                                disabled={busy}
                              >
                                {t('account_actions.ignore')}
                              </Button>
                              <Button
                                variant="danger"
                                size="xs"
                                onClick={() => confirmDelete(candidate)}
                                disabled={busy}
                              >
                                <IconTrash2 size={14} />
                                {t('account_actions.delete_auth_file')}
                              </Button>
                            </>
                          )}
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        ) : null}
      </WorkspaceListPanel>

      <Modal
        open={Boolean(evidenceCandidate)}
        title={t('account_actions.evidence_title')}
        width={780}
        onClose={() => setEvidenceCandidate(null)}
        footer={
          <div className={styles.modalFooterActions}>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => void handleCopyEvidence()}
              disabled={!evidenceText}
            >
              <IconCopy size={14} />
              {t('common.copy', { defaultValue: 'Copy' })}
            </Button>
            <Button variant="secondary" size="sm" onClick={() => setEvidenceCandidate(null)}>
              {t('common.close')}
            </Button>
          </div>
        }
      >
        {evidenceCandidate ? (
          <div className={styles.evidenceModalBody}>
            <div className={styles.evidenceMetaGrid}>
              <div className={styles.evidenceMetaItem}>
                <span>{t('account_actions.col_file')}</span>
                <code>{evidenceCandidate.authFileName}</code>
              </div>
              <div className={styles.evidenceMetaItem}>
                <span>{t('account_actions.evidence_status_code', { defaultValue: 'Status' })}</span>
                <code>{evidenceHeader.statusCode || '-'}</code>
              </div>
              <div className={styles.evidenceMetaItem}>
                <span>{t('account_actions.evidence_model', { defaultValue: 'Model' })}</span>
                <code>{evidenceHeader.model || '-'}</code>
              </div>
              <div className={styles.evidenceMetaItem}>
                <span>{t('account_actions.evidence_trace', { defaultValue: 'Trace' })}</span>
                <code>{evidenceHeader.traceId || '-'}</code>
              </div>
              <div className={styles.evidenceMetaItem}>
                <span>
                  {t('account_actions.evidence_first_seen', { defaultValue: 'First seen' })}
                </span>
                <code>{formatMs(evidenceCandidate.firstSeenAtMs)}</code>
              </div>
              <div className={styles.evidenceMetaItem}>
                <span>
                  {t('account_actions.evidence_last_seen', { defaultValue: 'Last seen' })}
                </span>
                <code>{formatMs(evidenceCandidate.lastSeenAtMs)}</code>
              </div>
              {evidenceHeader.endpoint ? (
                <div className={`${styles.evidenceMetaItem} ${styles.evidenceMetaWide}`}>
                  <span>
                    {t('account_actions.evidence_endpoint', { defaultValue: 'Endpoint' })}
                  </span>
                  <code>{evidenceHeader.endpoint}</code>
                </div>
              ) : null}
            </div>
            {evidenceCandidate.lastError ? (
              <div className={styles.evidenceErrorNote}>{evidenceCandidate.lastError}</div>
            ) : null}
            <pre className={styles.evidenceBox}>
              {evidenceText || t('account_actions.no_evidence')}
            </pre>
          </div>
        ) : null}
      </Modal>
    </div>
  );
}
