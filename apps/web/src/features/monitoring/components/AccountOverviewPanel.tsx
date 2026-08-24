import { Fragment, type ReactNode } from 'react';
import type { TFunction } from 'i18next';
import { DropdownMenu } from '@/components/ui/DropdownMenu';
import { Input } from '@/components/ui/Input';
import { Select, type SelectOption } from '@/components/ui/Select';
import {
  IconChevronDown,
  IconChevronUp,
  IconCrosshair,
  IconEye,
  IconEyeOff,
  IconInfo,
  IconMoreVertical,
  IconRefreshCw,
  IconSearch,
} from '@/components/ui/icons';
import {
  AccountExpandedDetails,
  AccountOverviewCard,
  AccountStatusBadge,
  AccountSummaryPrimary,
} from '@/features/monitoring/components/AccountOverviewCard';
import { PaginationControls } from '@/features/monitoring/components/MonitoringShared';
import {
  buildAccountSummaryMetrics,
  formatPercent,
  getAccountStatusTone,
  getSuccessRateClassName,
  type AccountQuotaState,
} from '@/features/monitoring/components/accountOverviewPresentation';
import { MonitoringPanel } from '@/features/monitoring/components/MonitoringPanel';
import {
  type AccountDisplayMode,
  type AccountSortKey,
  type AccountSortState,
  type MonitoringAccountAuthState,
  type MonitoringAccountOverviewMode,
} from '@/features/monitoring/accountOverviewState';
import type { MonitoringAccountRow } from '@/features/monitoring/hooks/useMonitoringData';
import type { StatusBarData } from '@/utils/recentRequests';
import styles from '../MonitoringCenterPage.module.scss';

type AccountOverviewColumn = {
  key: string;
  label: string;
  fullLabel?: string;
  sortKey?: AccountSortKey;
};

type PaginationState<T> = {
  currentPage: number;
  totalPages: number;
  pageItems: T[];
  startItem: number;
  endItem: number;
};

type AccountOverviewPanelProps = {
  embedded?: boolean;
  mode: MonitoringAccountOverviewMode;
  accountDisplayMode: AccountDisplayMode;
  searchInput: string;
  columns: AccountOverviewColumn[];
  rows: MonitoringAccountRow[];
  pagination: PaginationState<MonitoringAccountRow>;
  accountSort: AccountSortState;
  accountSortOptions: ReadonlyArray<SelectOption>;
  expandedAccounts: Record<string, boolean>;
  focusedAccountId: string | null;
  accountAuthStateByRowId: ReadonlyMap<string, MonitoringAccountAuthState>;
  accountStatusDataByRowId: ReadonlyMap<string, StatusBarData>;
  emptyAccountStatusData: StatusBarData;
  accountQuotaStatesByRowId: Record<string, AccountQuotaState>;
  accountPageSize: number;
  accountPageSizeOptions: readonly number[];
  accountOverviewScopeText: string;
  hasPrices: boolean;
  overallLoading: boolean;
  locale: string;
  emptyState: ReactNode;
  t: TFunction;
  onSearchChange: (value: string) => void;
  onRefreshAll: () => void | Promise<void>;
  onAccountSortKeyChange: (key: AccountSortKey) => void;
  onModeChange: (mode: MonitoringAccountOverviewMode) => void;
  onAccountDisplayModeChange: (mode: AccountDisplayMode) => void;
  onAccountSort: (sortKey: AccountSortKey) => void;
  onLoadAccountQuota: (rowId: string, force: boolean) => void | Promise<void>;
  onToggleExpanded: (rowId: string) => void;
  onFocusAccount: (row: MonitoringAccountRow) => void;
  onPageChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
};

export type AccountOverviewPanelActionsProps = Pick<
  AccountOverviewPanelProps,
  | 'mode'
  | 'accountDisplayMode'
  | 'searchInput'
  | 'accountSort'
  | 'accountSortOptions'
  | 'overallLoading'
  | 't'
  | 'onSearchChange'
  | 'onRefreshAll'
  | 'onAccountSortKeyChange'
  | 'onModeChange'
  | 'onAccountDisplayModeChange'
>;

const EMPTY_ACCOUNT_AUTH_STATE: MonitoringAccountAuthState = {
  files: [],
  enabledState: 'unavailable',
};

const getAccountColumnInfo = (columnKey: string, t: TFunction) => {
  if (columnKey === 'status') {
    return t('monitoring.account_overview_status_hint');
  }
  if (columnKey === 'success-rate') {
    return t('monitoring.account_overview_success_rate_hint');
  }
  return '';
};

function AccountColumnLabel({ column, t }: { column: AccountOverviewColumn; t: TFunction }) {
  const info = getAccountColumnInfo(column.key, t);
  const labelTitle = column.fullLabel ?? column.label;

  return (
    <span className={styles.tableHeaderWithInfo}>
      <span title={labelTitle}>{column.label}</span>
      {info ? (
        <span title={info}>
          <IconInfo size={13} className={styles.tableHeaderInfoIcon} aria-label={info} />
        </span>
      ) : null}
    </span>
  );
}

export function AccountOverviewPanelActions({
  mode,
  accountDisplayMode,
  searchInput,
  accountSort,
  accountSortOptions,
  overallLoading,
  t,
  onSearchChange,
  onRefreshAll,
  onAccountSortKeyChange,
  onModeChange,
  onAccountDisplayModeChange,
}: AccountOverviewPanelActionsProps) {
  const nextAccountDisplayMode: AccountDisplayMode =
    accountDisplayMode === 'masked' ? 'full' : 'masked';
  const AccountDisplayIcon = accountDisplayMode === 'masked' ? IconEyeOff : IconEye;
  const accountDisplayHint = t(
    accountDisplayMode === 'masked'
      ? 'monitoring.account_overview_show_full_accounts_hint'
      : 'monitoring.account_overview_show_masked_accounts_hint'
  );

  return (
    <div className={styles.accountOverviewHeaderActions}>
      <div className={styles.accountOverviewToolbarRow}>
        <div className={styles.accountOverviewSearchWrap}>
          <Input
            value={searchInput}
            onChange={(event) => onSearchChange(event.target.value)}
            placeholder={t('monitoring.account_overview_search_placeholder')}
            className={styles.accountOverviewSearchInput}
            rightElement={<IconSearch size={16} />}
            aria-label={t('monitoring.account_overview_search_placeholder')}
          />
        </div>
        <button
          type="button"
          className={styles.accountOverviewToolButton}
          onClick={() => void onRefreshAll()}
          disabled={overallLoading}
        >
          <IconRefreshCw
            size={15}
            className={overallLoading ? styles.refreshIconSpinning : styles.refreshIcon}
          />
          <span>{t('common.refresh')}</span>
        </button>
        <button
          type="button"
          className={[
            styles.accountOverviewToolButton,
            accountDisplayMode === 'full' ? styles.accountDisplayModeButtonActive : '',
          ]
            .filter(Boolean)
            .join(' ')}
          onClick={() => onAccountDisplayModeChange(nextAccountDisplayMode)}
          title={accountDisplayHint}
          aria-label={accountDisplayHint}
        >
          <AccountDisplayIcon size={15} aria-hidden="true" />
          <span>
            {t(
              accountDisplayMode === 'masked'
                ? 'monitoring.account_overview_account_display_masked'
                : 'monitoring.account_overview_account_display_full'
            )}
          </span>
        </button>
        <div className={styles.accountOverviewSortBar}>
          <Select
            className={styles.accountOverviewSortSelect}
            triggerClassName={styles.accountOverviewSortSelectTrigger}
            value={accountSort.key}
            options={accountSortOptions}
            onChange={(value) => onAccountSortKeyChange(value as AccountSortKey)}
            ariaLabel={t('monitoring.account_overview_sort_label')}
            fullWidth={false}
          />
        </div>

        <div className={`${styles.segmentedControl} ${styles.accountOverviewModeToggle}`}>
          <button
            type="button"
            className={`${styles.segmentButton} ${mode === 'table' ? styles.segmentButtonActive : ''}`}
            onClick={() => onModeChange('table')}
          >
            {t('monitoring.account_overview_view_mode_table')}
          </button>
          <button
            type="button"
            className={`${styles.segmentButton} ${mode === 'card' ? styles.segmentButtonActive : ''}`}
            onClick={() => onModeChange('card')}
          >
            {t('monitoring.account_overview_view_mode_card')}
          </button>
        </div>
      </div>
    </div>
  );
}

export function AccountOverviewPanel({
  embedded = false,
  mode,
  accountDisplayMode,
  searchInput,
  columns,
  rows,
  pagination,
  accountSort,
  accountSortOptions,
  expandedAccounts,
  focusedAccountId,
  accountAuthStateByRowId,
  accountStatusDataByRowId,
  emptyAccountStatusData,
  accountQuotaStatesByRowId,
  accountPageSize,
  accountPageSizeOptions,
  accountOverviewScopeText,
  hasPrices,
  overallLoading,
  locale,
  emptyState,
  t,
  onSearchChange,
  onRefreshAll,
  onAccountSortKeyChange,
  onModeChange,
  onAccountDisplayModeChange,
  onAccountSort,
  onLoadAccountQuota,
  onToggleExpanded,
  onFocusAccount,
  onPageChange,
  onPageSizeChange,
}: AccountOverviewPanelProps) {
  const actions = (
    <AccountOverviewPanelActions
      mode={mode}
      accountDisplayMode={accountDisplayMode}
      searchInput={searchInput}
      accountSort={accountSort}
      accountSortOptions={accountSortOptions}
      overallLoading={overallLoading}
      t={t}
      onSearchChange={onSearchChange}
      onRefreshAll={onRefreshAll}
      onAccountSortKeyChange={onAccountSortKeyChange}
      onModeChange={onModeChange}
      onAccountDisplayModeChange={onAccountDisplayModeChange}
    />
  );

  const content = (
    <>
      {mode === 'table' ? (
        <div className={`${styles.tableWrapper} ${styles.accountOverviewTableWrapper}`}>
          <table className={`${styles.table} ${styles.accountOverviewTable}`}>
            <colgroup>
              {columns.map((column) => (
                <col key={column.key} />
              ))}
            </colgroup>
            <thead>
              <tr>
                {columns.map((column) => {
                  const sortKey = column.sortKey;

                  if (!sortKey) {
                    return (
                      <th key={column.key}>
                        <AccountColumnLabel column={column} t={t} />
                      </th>
                    );
                  }

                  const isActive = accountSort.key === sortKey;
                  const SortIcon = isActive
                    ? accountSort.direction === 'desc'
                      ? IconChevronDown
                      : IconChevronUp
                    : null;

                  return (
                    <th
                      key={column.key}
                      aria-sort={
                        isActive
                          ? accountSort.direction === 'desc'
                            ? 'descending'
                            : 'ascending'
                          : 'none'
                      }
                    >
                      <button
                        type="button"
                        className={[
                          styles.sortableHeaderButton,
                          isActive ? styles.sortableHeaderButtonActive : '',
                        ]
                          .filter(Boolean)
                          .join(' ')}
                        onClick={() => onAccountSort(sortKey)}
                      >
                        <AccountColumnLabel column={column} t={t} />
                        <span className={styles.sortIndicator} aria-hidden="true">
                          {SortIcon ? <SortIcon size={14} /> : null}
                        </span>
                      </button>
                    </th>
                  );
                })}
              </tr>
            </thead>
            <tbody>
              {pagination.pageItems.map((row) => {
                const isExpanded = Boolean(expandedAccounts[row.id]);
                const isFocused = focusedAccountId === row.id;
                const authState = accountAuthStateByRowId.get(row.id) ?? EMPTY_ACCOUNT_AUTH_STATE;
                const statusTone = getAccountStatusTone(authState);
                const summaryMetrics = buildAccountSummaryMetrics(row, hasPrices, locale, t);
                const metricByKey = new Map(summaryMetrics.map((metric) => [metric.key, metric]));
                const rowClassName = [
                  styles.accountSummaryRow,
                  isFocused ? styles.focusedRow : '',
                  isExpanded ? styles.accountOverviewRowExpanded : '',
                  authState.enabledState === 'disabled' ? styles.accountOverviewRowDisabled : '',
                ]
                  .filter(Boolean)
                  .join(' ');
                const accountMenuItems = [
                  {
                    key: 'refresh-quota',
                    label: t('monitoring.account_overview_row_menu_refresh_quota'),
                    onClick: () => void onLoadAccountQuota(row.id, true),
                  },
                ];

                return (
                  <Fragment key={row.id}>
                    <tr className={rowClassName || undefined}>
                      <td>
                        <AccountSummaryPrimary
                          row={row}
                          expanded={isExpanded}
                          onToggle={() => onToggleExpanded(row.id)}
                          accountDisplayMode={accountDisplayMode}
                          statusTone={statusTone}
                        />
                      </td>
                      <td>
                        <AccountStatusBadge authState={authState} t={t} />
                      </td>
                      <td>{metricByKey.get('total-calls')?.value ?? '--'}</td>
                      <td className={metricByKey.get('success-calls')?.valueClassName}>
                        {metricByKey.get('success-calls')?.value ?? '--'}
                      </td>
                      <td className={metricByKey.get('failure-calls')?.valueClassName}>
                        {metricByKey.get('failure-calls')?.value ?? '--'}
                      </td>
                      <td className={getSuccessRateClassName(row.successRate)}>
                        {formatPercent(row.successRate)}
                      </td>
                      <td>{metricByKey.get('total-tokens')?.value ?? '--'}</td>
                      <td>{metricByKey.get('estimated-cost')?.value ?? '--'}</td>
                      <td>{metricByKey.get('latest-request-time')?.value ?? '--'}</td>
                      <td>
                        <div className={styles.accountActionGroup}>
                          <button
                            type="button"
                            className={styles.inlineActionButton}
                            onClick={() => onFocusAccount(row)}
                          >
                            <IconCrosshair size={13} aria-hidden="true" />
                            <span>
                              {isFocused
                                ? t('monitoring.restore_account_scope')
                                : t('monitoring.focus_account')}
                            </span>
                          </button>
                          <DropdownMenu
                            ariaLabel={t('monitoring.account_overview_row_menu_label')}
                            triggerClassName={styles.accountRowMenuButton}
                            triggerIcon={<IconMoreVertical size={15} aria-hidden="true" />}
                            items={accountMenuItems}
                          />
                        </div>
                      </td>
                    </tr>
                    {isExpanded ? (
                      <tr className={styles.accountDetailRow}>
                        <td colSpan={columns.length}>
                          <AccountExpandedDetails
                            row={row}
                            hasPrices={hasPrices}
                            locale={locale}
                            t={t}
                            summaryMetrics={summaryMetrics}
                            quotaState={accountQuotaStatesByRowId[row.id]}
                            onRefreshQuota={() => void onLoadAccountQuota(row.id, true)}
                            variant="table"
                          />
                        </td>
                      </tr>
                    ) : null}
                  </Fragment>
                );
              })}
              {rows.length === 0 ? (
                <tr>
                  <td colSpan={columns.length}>{emptyState}</td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      ) : rows.length > 0 ? (
        <div className={styles.accountOverviewCardGrid}>
          {pagination.pageItems.map((row) => {
            const authState = accountAuthStateByRowId.get(row.id) ?? EMPTY_ACCOUNT_AUTH_STATE;

            return (
              <AccountOverviewCard
                key={row.id}
                row={row}
                authState={authState}
                hasPrices={hasPrices}
                locale={locale}
                t={t}
                accountDisplayMode={accountDisplayMode}
                isExpanded={Boolean(expandedAccounts[row.id])}
                isFocused={focusedAccountId === row.id}
                statusData={accountStatusDataByRowId.get(row.id) ?? emptyAccountStatusData}
                scopeText={accountOverviewScopeText}
                quotaState={accountQuotaStatesByRowId[row.id]}
                onToggle={() => onToggleExpanded(row.id)}
                onFocus={() => onFocusAccount(row)}
                onRefreshQuota={() => void onLoadAccountQuota(row.id, true)}
              />
            );
          })}
        </div>
      ) : (
        emptyState
      )}
      <PaginationControls
        count={rows.length}
        currentPage={pagination.currentPage}
        totalPages={pagination.totalPages}
        startItem={pagination.startItem}
        endItem={pagination.endItem}
        pageSize={accountPageSize}
        pageSizeOptions={accountPageSizeOptions}
        onPageChange={onPageChange}
        onPageSizeChange={onPageSizeChange}
        t={t}
      />
    </>
  );

  if (embedded) {
    return content;
  }

  return (
    <MonitoringPanel
      title={
        <span className={styles.panelTitleWithHint}>
          {t('monitoring.account_overview_title')}
          <span title={t('monitoring.account_overview_description')}>
            <IconInfo
              size={14}
              className={styles.panelTitleHintIcon}
              aria-label={t('monitoring.account_overview_description')}
            />
          </span>
        </span>
      }
      className={styles.accountPanel}
      extra={actions}
    >
      {content}
    </MonitoringPanel>
  );
}
