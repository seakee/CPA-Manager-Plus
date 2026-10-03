import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { IconRefreshCw } from '@/components/ui/icons';
import { SegmentedTabs } from '@/components/ui/SegmentedTabs';
import {
  reconnectApi,
  reconnectErrorMessage,
  type ReconnectOutage,
  type ReconnectStatus,
} from '@/services/api/reconnect';
import { PaginationControls } from '@/features/monitoring/components/MonitoringShared';
import { formatInZone, formatWaiting, providerLabel } from '../model/reconnectFormat';
import styles from './ReconnectSettingsSection.module.scss';

type Filter = ReconnectStatus | 'all';

const DAY_SECONDS = 86400;

const STATUS_CLASS: Record<ReconnectStatus, string> = {
  pending: styles.statusPending,
  completed: styles.statusCompleted,
  resolved: styles.statusResolved,
  expired: '',
};

interface Props {
  base: string;
  managementKey: string;
  timeZone: string;
  /** Bumped by the parent after sending a link, to refresh at once. */
  version: number;
}

const PAGE_SIZES = [20, 50, 100];

/** Reconnect requests of the last 30 days; refreshes every minute. */
export function ReconnectRequestsTable({ base, managementKey, timeZone, version }: Props) {
  const { t } = useTranslation();
  const [rows, setRows] = useState<ReconnectOutage[]>([]);
  const [filter, setFilter] = useState<Filter>('pending');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(PAGE_SIZES[0]);

  const load = useCallback(async () => {
    if (!base) return;
    setLoading(true);
    try {
      setRows(await reconnectApi.listRequests(base, managementKey));
      setError('');
    } catch (err) {
      setError(reconnectErrorMessage(err));
    } finally {
      setLoading(false);
      setLoaded(true);
    }
  }, [base, managementKey]);

  useEffect(() => {
    void load();
    const refresh = window.setInterval(() => void load(), 60_000);
    return () => window.clearInterval(refresh);
  }, [load, version]);

  useEffect(() => {
    const tick = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(tick);
  }, []);

  const labels: Record<Filter, string> = {
    pending: t('reconnect.status_pending', { defaultValue: 'Waiting' }),
    completed: t('reconnect.status_completed', { defaultValue: 'Reconnected' }),
    resolved: t('reconnect.status_resolved', { defaultValue: 'Recovered' }),
    expired: t('reconnect.status_expired', { defaultValue: 'Not used' }),
    all: t('reconnect.status_all', { defaultValue: 'All' }),
  };
  const filters: Filter[] = ['pending', 'completed', 'resolved', 'expired', 'all'];
  const matching = filter === 'all' ? rows : rows.filter((row) => row.status === filter);
  // Same paging as the accounts list. The page is clamped when a refresh shrinks the list.
  const totalPages = Math.max(1, Math.ceil(matching.length / pageSize));
  const currentPage = Math.min(page, totalPages);
  const pageStart = (currentPage - 1) * pageSize;
  const visible = matching.slice(pageStart, pageStart + pageSize);
  const count = (f: Filter) =>
    f === 'all' ? rows.length : rows.filter((row) => row.status === f).length;

  // When the clock stops: waiting -> now, otherwise when it was closed.
  const waitedSeconds = (row: ReconnectOutage) => {
    const end = row.status === 'pending' ? now : row.closedAtMs || row.lastMessageAtMs;
    return (end - row.firstNotifiedAtMs) / 1000;
  };

  return (
    <div className={styles.group}>
      <div className={styles.tableHeader}>
        <div className={styles.sectionHeaderText}>
          <h4 className={styles.groupTitle}>
            {t('reconnect.requests_title', { defaultValue: 'Reconnect Requests' })}
          </h4>
          <p className={styles.sectionHint}>
            {t('reconnect.requests_hint', {
              defaultValue: 'Last 30 days. Times in {{timeZone}}.',
              timeZone,
            })}
          </p>
        </div>
        <Button variant="ghost" size="sm" onClick={() => void load()} disabled={loading}>
          <IconRefreshCw size={14} />
          {t('common.refresh')}
        </Button>
      </div>
      <SegmentedTabs<Filter>
        items={filters.map((f) => ({ id: f, label: `${labels[f]} · ${count(f)}` }))}
        activeTab={filter}
        onChange={(next) => {
          setFilter(next);
          setPage(1);
        }}
        ariaLabel={t('reconnect.filter', { defaultValue: 'Filter by status' })}
      />
      {error ? (
        <div className={styles.errorState} role="alert">
          <strong>{t('reconnect.load_failed', { defaultValue: 'Load failed' })}</strong>
          <span>{error}</span>
        </div>
      ) : null}
      {error && rows.length === 0 ? null : visible.length === 0 && loaded ? (
        <div className={styles.emptyState}>
          {t('reconnect.empty', { defaultValue: 'No reconnect requests' })}
        </div>
      ) : (
        <div className={styles.tableWrap}>
          <table className={styles.table}>
            <thead>
              <tr>
                <th>{t('reconnect.col_email', { defaultValue: 'Email' })}</th>
                <th>{t('reconnect.col_login', { defaultValue: 'Login' })}</th>
                {filter === 'all' ? (
                  <th>{t('reconnect.col_status', { defaultValue: 'Status' })}</th>
                ) : null}
                <th>{t('reconnect.col_waiting', { defaultValue: 'Waiting for' })}</th>
                <th>{t('reconnect.col_first', { defaultValue: 'First notified' })}</th>
                <th>{t('reconnect.col_reminders', { defaultValue: 'Reminders' })}</th>
                <th>
                  {filter === 'pending'
                    ? t('reconnect.col_expires', { defaultValue: 'Link expires' })
                    : t('reconnect.col_closed', { defaultValue: 'Closed' })}
                </th>
              </tr>
            </thead>
            <tbody>
              {visible.map((row) => {
                const waited = waitedSeconds(row);
                const live = row.status === 'pending';
                return (
                  <tr key={row.id}>
                    <td>
                      {row.email}
                      {row.manual ? (
                        <span className={styles.muted}>
                          {' '}
                          {t('reconnect.sent_by_admin', { defaultValue: '(sent by admin)' })}
                        </span>
                      ) : null}
                    </td>
                    <td>{providerLabel(row.provider)}</td>
                    {filter === 'all' ? (
                      <td>
                        <span
                          className={[styles.statusBadge, STATUS_CLASS[row.status]]
                            .filter(Boolean)
                            .join(' ')}
                        >
                          {labels[row.status]}
                        </span>
                      </td>
                    ) : null}
                    <td
                      className={[
                        styles.waiting,
                        live && waited >= DAY_SECONDS ? styles.waitingLong : '',
                        live ? '' : styles.muted,
                      ]
                        .filter(Boolean)
                        .join(' ')}
                    >
                      {formatWaiting(waited)}
                    </td>
                    <td>{formatInZone(row.firstNotifiedAtMs, timeZone)}</td>
                    <td>{row.reminders}</td>
                    <td>{formatInZone(live ? row.expiresAtMs : row.closedAtMs, timeZone)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          <div className={styles.pagination}>
            <PaginationControls
              count={matching.length}
              currentPage={currentPage}
              totalPages={totalPages}
              startItem={pageStart + 1}
              endItem={pageStart + visible.length}
              pageSize={pageSize}
              pageSizeOptions={PAGE_SIZES}
              onPageChange={setPage}
              onPageSizeChange={(next) => {
                setPageSize(next);
                setPage(1);
              }}
              t={t}
            />
          </div>
        </div>
      )}
    </div>
  );
}
