import type { ReactNode } from 'react';
import type { TFunction } from 'i18next';
import { Button } from '@/components/ui/Button';
import { Select } from '@/components/ui/Select';
import { IconRefreshCw, IconTrash2 } from '@/components/ui/icons';
import { FailureDetailsTooltip } from '@/features/monitoring/components/FailureDetailsTooltip';
import {
  type CodexInspectionAction,
  type CodexInspectionResultItem,
  type CodexInspectionRunResult,
  isExecutableAction,
} from '@/features/monitoring/codexInspection';
import {
  type CodexInspectionPaginationState,
  formatActionLabel,
  formatCurrentStateLabel,
  getInspectionProbePresentation,
  getVisibleActionFilters,
  shouldShowInspectionConclusionReason,
  summarizeInspectionError,
  type ActionFilter,
  type HandlingFilter,
  type InspectionProbeSource,
  type InspectionProbeState,
} from '@/features/monitoring/model/codexInspectionPresentation';
import { getCodexPlanLabel } from '@/features/monitoring/components/accountOverviewPresentation';
import { CodexInspectionQuotaWindows } from '@/features/monitoring/components/CodexInspectionQuotaWindows';
import { Panel } from '@/features/monitoring/components/CodexInspectionPanels';
import { useNotificationStore } from '@/stores';
import { copyToClipboard } from '@/utils/clipboard';
import styles from '../CodexInspectionPage.module.scss';

type CodexInspectionResultsPanelProps = {
  result: CodexInspectionRunResult | null;
  filteredResults: CodexInspectionResultItem[];
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
  xaiInferenceEnabled?: boolean;
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
  pendingActionCount,
  manualActionCount = 0,
  reauthActionCount = 0,
  filterCounts,
  actionFilter,
  pagination,
  pageSize,
  pageSizeOptions,
  executing,
  isInspectionInFlight,
  t,
  title,
  xaiInferenceEnabled = false,
  onActionFilterChange,
  onPageChange,
  onPageSizeChange,
  onExecutePlanned,
  onExecuteSingle,
  onReauthAccount,
  onDeleteReauthPlanned,
  onDeleteReauthSingle,
  filterLabel,
  renderOperation,
}: CodexInspectionResultsPanelProps) {
  const showNotification = useNotificationStore((state) => state.showNotification);
  const reauthDeleteAvailable = Boolean(onDeleteReauthPlanned);
  const headerButtonText = executing
    ? t('monitoring.codex_inspection_executing')
    : pendingActionCount > 0
      ? t('monitoring.codex_inspection_execute_now')
      : manualActionCount > 0 && !reauthDeleteAvailable
        ? t('monitoring.codex_inspection_pending_reauth_count', { count: manualActionCount })
        : t('monitoring.codex_inspection_no_executable_actions');
  const renderOperationForItem = (item: CodexInspectionResultItem) =>
    renderOperation ? (
      renderOperation(item)
    ) : isExecutableAction(item) ? (
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
            {t(item.provider === 'xai' ? 'auth_login.xai_oauth_button' : 'codex_reauth.button')}
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
    ) : null;
  const visibleActionFilters = getVisibleActionFilters();
  const handleCopyFailureDetails = async (text: string) => {
    const copied = await copyToClipboard(text);
    showNotification(
      t(copied ? 'notification.link_copied' : 'notification.copy_failed'),
      copied ? 'success' : 'error'
    );
  };

  const probeSourceLabel = (source: InspectionProbeSource) =>
    t(`monitoring.codex_inspection_probe_source_${source}`);
  const probeStateLabel = (state: InspectionProbeState) =>
    t(`monitoring.codex_inspection_probe_state_${state}`);
  const probeStateClass: Record<InspectionProbeState, string> = {
    success: styles.probeToneSuccess,
    failed: styles.probeToneFailed,
    skipped: styles.probeToneSkipped,
  };

  return (
    <Panel title={title ?? t('monitoring.codex_inspection_results_title')}>
      {result ? (
        <>
          <div className={styles.resultsToolbar}>
            <div className={styles.filterRow}>
              <div
                className={styles.segmentedGroup}
                role="group"
                aria-label={t('monitoring.codex_inspection_action_filter_label')}
              >
                <div className={styles.segmentedControl}>
                  {visibleActionFilters.map((filter) => {
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

            <div className={styles.resultsToolbarActions}>
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
          </div>

          <div className={styles.resultCardList}>
            {filteredResults.length > 0 ? (
              filteredResults.map((item) => {
                const isXai = item.provider.trim().toLowerCase() === 'xai';
                const planLabel = isXai ? null : getCodexPlanLabel(item.planType, t);
                const quotaWindows = item.quotaWindows ?? [];
                const errorText = item.errorDetail || item.error;
                const errorSummary = summarizeInspectionError(item, t, {
                  xaiInferenceEnabled,
                });
                const conclusionReason = item.actionReason?.startsWith('monitoring.')
                  ? t(item.actionReason)
                  : item.actionReason;
                const probe = getInspectionProbePresentation(item, {
                  xaiInferenceEnabled,
                });
                const probeStatusText = probe.statusCode !== null ? `HTTP ${probe.statusCode}` : '';
                const failureDetailLines = [
                  errorSummary,
                  errorText && errorText !== errorSummary ? errorText : '',
                ].filter(Boolean);
                const failureCopyText = [probeStatusText, ...failureDetailLines]
                  .filter(Boolean)
                  .join('\n');
                const hasFailureDetails = probe.state === 'failed' && failureDetailLines.length > 0;
                const operation = renderOperationForItem(item);
                return (
                  <article
                    key={item.key}
                    className={styles.resultCard}
                    aria-label={item.displayAccount}
                  >
                    <section
                      className={`${styles.resultCardSection} ${styles.resultCredentialSection}`}
                    >
                      <div className={styles.credentialBadges}>
                        <span
                          className={`${styles.providerBadge} ${
                            isXai ? styles.providerBadgeXai : styles.providerBadgeCodex
                          }`}
                        >
                          {isXai
                            ? t('monitoring.codex_inspection_target_xai')
                            : t('monitoring.codex_inspection_target_codex')}
                        </span>
                        <span
                          className={`${styles.stateChip} ${
                            item.disabled ? styles.stateDisabled : styles.stateEnabled
                          }`}
                        >
                          {formatCurrentStateLabel(item, t)}
                        </span>
                        {planLabel ? <span className={styles.planBadge}>{planLabel}</span> : null}
                      </div>
                      <div className={styles.primaryCell}>
                        <strong className={styles.primaryAccount} title={item.displayAccount}>
                          {item.displayAccount}
                        </strong>
                        <small className={styles.primaryFile} title={item.fileName}>
                          {item.fileName}
                        </small>
                      </div>
                    </section>

                    <section className={styles.resultCardSection}>
                      <span className={styles.resultSectionLabel}>
                        {probeSourceLabel(probe.source)}
                      </span>
                      {hasFailureDetails ? (
                        <FailureDetailsTooltip
                          ariaLabel={[probeStateLabel(probe.state), probeStatusText, errorSummary]
                            .filter(Boolean)
                            .join(' · ')}
                          statusText={probeStatusText}
                          detailLines={failureDetailLines}
                          copyText={failureCopyText}
                          copyLabel={t('common.copy')}
                          onCopy={handleCopyFailureDetails}
                        >
                          <span
                            className={`${styles.probeStateLine} ${probeStateClass[probe.state]}`}
                          >
                            <span className={styles.probeStateDot} aria-hidden="true" />
                            <strong>{probeStateLabel(probe.state)}</strong>
                          </span>
                        </FailureDetailsTooltip>
                      ) : (
                        <div className={`${styles.probeStateLine} ${probeStateClass[probe.state]}`}>
                          <span className={styles.probeStateDot} aria-hidden="true" />
                          <strong>{probeStateLabel(probe.state)}</strong>
                        </div>
                      )}
                      {probe.statusCode !== null ? (
                        <small className={styles.probeHttpStatus}>HTTP {probe.statusCode}</small>
                      ) : null}
                    </section>

                    <section className={`${styles.resultCardSection} ${styles.resultQuotaSection}`}>
                      <CodexInspectionQuotaWindows
                        windows={quotaWindows}
                        fallbackUsedPercent={item.usedPercent}
                        t={t}
                      />
                    </section>

                    <section
                      className={`${styles.resultCardSection} ${styles.resultConclusionSection}`}
                    >
                      <span className={`${styles.actionBadge} ${actionToneClass[item.action]}`}>
                        {formatActionLabel(item.action, t)}
                      </span>
                      {conclusionReason && shouldShowInspectionConclusionReason(item) ? (
                        <small className={styles.primaryReason}>{conclusionReason}</small>
                      ) : null}
                      {item.observedHeaderEvidence?.length ? (
                        <small className={styles.primaryEvidence}>
                          {t('monitoring.codex_inspection_observed_header_evidence')}:{' '}
                          {item.observedHeaderEvidence.join(' · ')}
                        </small>
                      ) : null}
                      {operation ? (
                        <div className={styles.resultCardOperation}>{operation}</div>
                      ) : null}
                    </section>
                  </article>
                );
              })
            ) : (
              <div className={styles.emptyBlockSmall}>
                {t('monitoring.codex_inspection_no_pending_actions')}
              </div>
            )}
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
