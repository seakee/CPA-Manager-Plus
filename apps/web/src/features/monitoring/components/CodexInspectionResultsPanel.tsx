import type { ReactNode } from 'react';
import type { TFunction } from 'i18next';
import { Button } from '@/components/ui/Button';
import { Select } from '@/components/ui/Select';
import { IconRefreshCw, IconTrash2 } from '@/components/ui/icons';
import {
  type CodexInspectionAction,
  type CodexInspectionResultItem,
  type CodexInspectionRunResult,
  isExecutableAction,
} from '@/features/monitoring/codexInspection';
import {
  ACTION_FILTERS,
  HANDLING_FILTERS,
  type CodexInspectionPaginationState,
  formatActionLabel,
  formatCurrentStateLabel,
  summarizeInspectionError,
  type ActionFilter,
  type HandlingFilter,
} from '@/features/monitoring/model/codexInspectionPresentation';
import { getCodexPlanLabel } from '@/features/monitoring/components/accountOverviewPresentation';
import { CodexInspectionQuotaWindows } from '@/features/monitoring/components/CodexInspectionQuotaWindows';
import { Panel } from '@/features/monitoring/components/CodexInspectionPanels';
import styles from '../CodexInspectionPage.module.scss';

type CodexInspectionResultsPanelProps = {
  result: CodexInspectionRunResult | null;
  filteredResults: CodexInspectionResultItem[];
  suggestedResults: CodexInspectionResultItem[];
  pendingActionCount: number;
  manualActionCount?: number;
  reauthActionCount?: number;
  handlingFilterCounts: Record<HandlingFilter, number>;
  filterCounts: Record<ActionFilter, number>;
  handlingFilter: HandlingFilter;
  actionFilter: ActionFilter;
  pagination: CodexInspectionPaginationState<CodexInspectionResultItem>;
  pageSize: number;
  pageSizeOptions: readonly number[];
  executing: boolean;
  isInspectionInFlight: boolean;
  t: TFunction;
  title?: string;
  subtitle?: string;
  stateHeaderLabel?: string;
  onActionFilterChange: (filter: ActionFilter) => void;
  onHandlingFilterChange: (filter: HandlingFilter) => void;
  onPageChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
  onExecutePlanned: () => void;
  onExecuteSingle: (item: CodexInspectionResultItem) => void;
  onReauthAccount?: (item: CodexInspectionResultItem) => void;
  onDeleteReauthPlanned?: () => void;
  onDeleteReauthSingle?: (item: CodexInspectionResultItem) => void;
  filterLabel: (filter: ActionFilter) => string;
  handlingFilterLabel: (filter: HandlingFilter) => string;
  renderOperation?: (item: CodexInspectionResultItem) => ReactNode;
};

const actionToneClass: Record<CodexInspectionAction, string> = {
  keep: styles.actionKeep,
  delete: styles.actionDelete,
  disable: styles.actionDisable,
  enable: styles.actionEnable,
  reauth: styles.actionReauth,
};

export function CodexInspectionResultsPanel({
  result,
  filteredResults,
  suggestedResults,
  pendingActionCount,
  manualActionCount = 0,
  reauthActionCount = 0,
  handlingFilterCounts,
  filterCounts,
  handlingFilter,
  actionFilter,
  pagination,
  pageSize,
  pageSizeOptions,
  executing,
  isInspectionInFlight,
  t,
  title,
  subtitle,
  stateHeaderLabel,
  onActionFilterChange,
  onHandlingFilterChange,
  onPageChange,
  onPageSizeChange,
  onExecutePlanned,
  onExecuteSingle,
  onReauthAccount,
  onDeleteReauthPlanned,
  onDeleteReauthSingle,
  filterLabel,
  handlingFilterLabel,
  renderOperation,
}: CodexInspectionResultsPanelProps) {
  const reauthDeleteAvailable = Boolean(onDeleteReauthPlanned);
  const headerButtonText = executing
    ? t('monitoring.codex_inspection_executing')
    : pendingActionCount > 0
      ? t('monitoring.codex_inspection_execute_now')
      : manualActionCount > 0 && !reauthDeleteAvailable
        ? t('monitoring.codex_inspection_pending_reauth_count', { count: manualActionCount })
        : t('monitoring.codex_inspection_no_executable_actions');

  return (
    <Panel
      title={title ?? t('monitoring.codex_inspection_results_title')}
      subtitle={subtitle ?? t('monitoring.codex_inspection_results_desc')}
      extra={
        <div className={styles.resultsHeaderActions}>
          {onDeleteReauthPlanned ? (
            <Button
              variant="danger"
              size="sm"
              onClick={onDeleteReauthPlanned}
              disabled={!result || isInspectionInFlight || executing || reauthActionCount === 0}
            >
              <IconTrash2 size={14} />
              {t('monitoring.codex_inspection_delete_reauth_count', {
                count: reauthActionCount,
              })}
            </Button>
          ) : null}
          <Button
            variant={pendingActionCount > 0 ? 'danger' : 'secondary'}
            size="sm"
            onClick={onExecutePlanned}
            loading={executing}
            disabled={!result || isInspectionInFlight || executing || pendingActionCount === 0}
          >
            {headerButtonText}
          </Button>
        </div>
      }
    >
      {result ? (
        <>
          <div className={styles.filterRow}>
            <div
              className={styles.segmentedGroup}
              role="group"
              aria-label={t('monitoring.codex_inspection_handling_filter_label')}
            >
              <span className={styles.segmentedLabel}>
                {t('monitoring.codex_inspection_handling_filter_label')}
              </span>
              <div className={styles.segmentedControl}>
                {HANDLING_FILTERS.map((filter) => {
                  const count = handlingFilterCounts[filter];
                  const isActive = handlingFilter === filter;
                  return (
                    <button
                      key={filter}
                      type="button"
                      className={`${styles.segmentButton} ${isActive ? styles.segmentButtonActive : ''}`}
                      onClick={() => onHandlingFilterChange(filter)}
                    >
                      <span>{handlingFilterLabel(filter)}</span>
                      <span className={styles.segmentCount}>{count}</span>
                    </button>
                  );
                })}
              </div>
            </div>

            <div
              className={styles.segmentedGroup}
              role="group"
              aria-label={t('monitoring.codex_inspection_action_filter_label')}
            >
              <span className={styles.segmentedLabel}>
                {t('monitoring.codex_inspection_action_filter_label')}
              </span>
              <div className={styles.segmentedControl}>
                {ACTION_FILTERS.map((filter) => {
                  const count = filterCounts[filter];
                  const isActive = actionFilter === filter;
                  return (
                    <button
                      key={filter}
                      type="button"
                      className={`${styles.segmentButton} ${isActive ? styles.segmentButtonActive : ''}`}
                      onClick={() => onActionFilterChange(filter)}
                    >
                      <span>{filterLabel(filter)}</span>
                      <span className={styles.segmentCount}>{count}</span>
                    </button>
                  );
                })}
              </div>
            </div>
          </div>

          <div className={styles.tableWrap}>
            <table className={styles.table}>
              <colgroup>
                <col className={styles.accountColumn} />
                <col className={styles.stateColumn} />
                <col className={styles.httpColumn} />
                <col className={styles.usageColumn} />
                <col className={styles.actionColumn} />
                <col className={styles.operationColumn} />
              </colgroup>
              <thead>
                <tr>
                  <th>{t('monitoring.account_label')}</th>
                  <th>{stateHeaderLabel ?? t('monitoring.codex_inspection_current_state')}</th>
                  <th>{t('monitoring.codex_inspection_http_status')}</th>
                  <th>{t('monitoring.codex_inspection_used_percent')}</th>
                  <th>{t('monitoring.codex_inspection_next_action')}</th>
                  <th>{t('common.action')}</th>
                </tr>
              </thead>
              <tbody>
                {filteredResults.length > 0 ? (
                  filteredResults.map((item) => {
                    const planLabel = getCodexPlanLabel(item.planType, t);
                    const quotaWindows = item.quotaWindows ?? [];
                    const errorText = item.errorDetail || item.error;
                    const errorSummary = summarizeInspectionError(item, t);
                    const operation = renderOperation?.(item);

                    return (
                      <tr key={item.key}>
                        <td>
                          <div className={styles.primaryCell}>
                            <span className={styles.primaryAccount}>{item.displayAccount}</span>
                            <small className={styles.primaryFile}>
                              {item.fileName}
                              {item.authIndex ? (
                                <span
                                  className={styles.primaryIndex}
                                >{` \u00b7 #${item.authIndex}`}</span>
                              ) : null}
                            </small>
                            {planLabel ? (
                              <span className={styles.planBadge}>
                                {t('codex_quota.plan_label')}: {planLabel}
                              </span>
                            ) : null}
                            {item.actionReason ? (
                              <small className={styles.primaryReason}>{item.actionReason}</small>
                            ) : null}
                            {item.observedHeaderEvidence?.length ? (
                              <small className={styles.primaryEvidence}>
                                {t('monitoring.codex_inspection_observed_header_evidence')}:{' '}
                                {item.observedHeaderEvidence.join(' · ')}
                              </small>
                            ) : null}
                            {errorSummary ? (
                              <small className={styles.primaryError}>{errorSummary}</small>
                            ) : null}
                            {errorText && errorText !== errorSummary ? (
                              <details className={styles.rawDetail}>
                                <summary>{t('monitoring.codex_inspection_raw_response')}</summary>
                                <pre>{errorText}</pre>
                              </details>
                            ) : null}
                          </div>
                        </td>
                        <td>
                          <span
                            className={`${styles.stateChip} ${
                              item.disabled ? styles.stateDisabled : styles.stateEnabled
                            }`}
                          >
                            {formatCurrentStateLabel(item, t)}
                          </span>
                        </td>
                        <td className={styles.monoCell}>
                          {item.statusCode === null ? '--' : item.statusCode}
                        </td>
                        <td>
                          <CodexInspectionQuotaWindows
                            windows={quotaWindows}
                            fallbackUsedPercent={item.usedPercent}
                            t={t}
                          />
                        </td>
                        <td>
                          <span className={`${styles.actionBadge} ${actionToneClass[item.action]}`}>
                            {formatActionLabel(item.action, t)}
                          </span>
                        </td>
                        <td>
                          {operation ??
                            (isExecutableAction(item) ? (
                              <Button
                                size="sm"
                                variant={item.action === 'delete' ? 'danger' : 'secondary'}
                                onClick={() => onExecuteSingle(item)}
                                disabled={isInspectionInFlight || executing}
                              >
                                {formatActionLabel(item.action, t)}
                              </Button>
                            ) : item.action === 'reauth' ? (
                              <div className={styles.resultsHeaderActions}>
                                {onReauthAccount ? (
                                  <Button
                                    size="sm"
                                    variant="secondary"
                                    onClick={() => onReauthAccount(item)}
                                    disabled={isInspectionInFlight || executing}
                                  >
                                    <IconRefreshCw size={14} />
                                    {t(
                                      item.provider === 'xai'
                                        ? 'auth_login.xai_oauth_button'
                                        : 'codex_reauth.button'
                                    )}
                                  </Button>
                                ) : (
                                  <span className={styles.primaryReason}>
                                    {t('monitoring.codex_inspection_manual_required')}
                                  </span>
                                )}
                                {onDeleteReauthSingle ? (
                                  <Button
                                    size="sm"
                                    variant="danger"
                                    onClick={() => onDeleteReauthSingle(item)}
                                    disabled={isInspectionInFlight || executing}
                                  >
                                    <IconTrash2 size={14} />
                                    {t('monitoring.codex_inspection_action_delete')}
                                  </Button>
                                ) : null}
                              </div>
                            ) : (
                              <span className={styles.primaryReason}>
                                {t('monitoring.codex_inspection_no_action')}
                              </span>
                            ))}
                        </td>
                      </tr>
                    );
                  })
                ) : (
                  <tr>
                    <td colSpan={6}>
                      <div className={styles.emptyBlockSmall}>
                        {suggestedResults.length === 0
                          ? t('monitoring.codex_inspection_no_pending_actions')
                          : t('monitoring.codex_inspection_no_pending_actions')}
                      </div>
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
          {pagination.totalPages > 1 ? (
            <div className={styles.resultPaginationBar}>
              <div className={styles.resultPaginationInfo}>
                {t('monitoring.pagination_info', {
                  current: pagination.currentPage,
                  total: pagination.totalPages,
                  start: pagination.startItem,
                  end: pagination.endItem,
                  count: pagination.count,
                })}
              </div>
              <div className={styles.resultPaginationControls}>
                <div className={styles.resultPageSizeField}>
                  <span>{t('monitoring.page_size_label')}</span>
                  <Select
                    className={styles.resultPageSizeSelect}
                    triggerClassName={styles.resultPageSizeSelectTrigger}
                    value={String(pageSize)}
                    options={pageSizeOptions.map((size) => ({
                      value: String(size),
                      label: t('monitoring.page_size_option', { count: size }),
                    }))}
                    onChange={(value) => {
                      const parsed = Number.parseInt(value, 10);
                      onPageSizeChange(Number.isFinite(parsed) && parsed > 0 ? parsed : pageSize);
                    }}
                    ariaLabel={t('monitoring.page_size_label')}
                    fullWidth={false}
                  />
                </div>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => onPageChange(Math.max(1, pagination.currentPage - 1))}
                  disabled={pagination.currentPage <= 1}
                >
                  {t('monitoring.pagination_prev')}
                </Button>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() =>
                    onPageChange(Math.min(pagination.totalPages, pagination.currentPage + 1))
                  }
                  disabled={pagination.currentPage >= pagination.totalPages}
                >
                  {t('monitoring.pagination_next')}
                </Button>
              </div>
            </div>
          ) : null}
        </>
      ) : (
        <div className={styles.emptyBlock}>{t('monitoring.codex_inspection_empty')}</div>
      )}
    </Panel>
  );
}
