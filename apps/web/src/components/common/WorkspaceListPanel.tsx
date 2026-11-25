import type { ReactNode } from 'react';
import { SegmentedTabs, type SegmentedTabItem } from '@/components/ui/SegmentedTabs';
import { PaginationFooter, type PaginationFooterProps } from './PaginationFooter';
import styles from './WorkspaceListPanel.module.scss';

export type WorkspaceListViewModeConfig<Mode extends string = 'table' | 'grid'> = {
  value: Mode;
  onChange: (mode: Mode) => void;
  tableLabel: ReactNode;
  cardsLabel: ReactNode;
  tableId?: Mode;
  cardsId?: Mode;
  ariaLabel: string;
  idBase?: string;
  hidden?: boolean;
  tableTestId?: string;
  cardsTestId?: string;
};

export type WorkspaceListPanelProps<Mode extends string = 'table' | 'grid'> = {
  as?: 'section' | 'div';
  id?: string;
  role?: string;
  ariaLabelledby?: string;
  showHeader?: boolean;
  headerLeading?: ReactNode;
  summary?: ReactNode;
  hint?: ReactNode;
  selectionBadge?: ReactNode;
  selectionTools?: ReactNode;
  selectionActive?: boolean;
  headerActions?: ReactNode;
  viewMode?: WorkspaceListViewModeConfig<Mode>;
  banner?: ReactNode;
  loading?: boolean;
  loadingFallback?: ReactNode;
  empty?: boolean;
  emptyFallback?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  pagination?: PaginationFooterProps | null;
  bodyPadding?: 'none' | 'gutter';
  className?: string;
  headerClassName?: string;
  bodyClassName?: string;
  testId?: string;
  headerTestId?: string;
};
const hasContent = (value: ReactNode) =>
  value !== null && value !== undefined && value !== false;

export function WorkspaceListPanel<Mode extends string = 'table' | 'grid'>({
  as: Component = 'section',
  id,
  role,
  ariaLabelledby,
  showHeader = true,
  headerLeading,
  summary,
  hint,
  selectionBadge,
  selectionTools,
  selectionActive = false,
  headerActions,
  viewMode,
  banner,
  loading = false,
  loadingFallback,
  empty = false,
  emptyFallback,
  children,
  footer,
  pagination,
  bodyPadding = 'none',
  className = '',
  headerClassName = '',
  bodyClassName = '',
  testId,
  headerTestId,
}: WorkspaceListPanelProps<Mode>) {
  const showViewMode = Boolean(viewMode && !viewMode.hidden);
  const hasHeaderMeta = hasContent(summary) || hasContent(hint);
  const hasHeaderLeft =
    hasContent(headerLeading) ||
    hasHeaderMeta ||
    hasContent(selectionBadge) ||
    hasContent(selectionTools);
  const hasHeaderRight = hasContent(headerActions) || showViewMode;
  const shouldRenderHeader = showHeader && (hasHeaderLeft || hasHeaderRight);

  const rootClasses = [styles.root, className].filter(Boolean).join(' ');
  const headerClasses = [
    styles.header,
    selectionActive ? styles.headerActive : '',
    headerClassName,
  ]
    .filter(Boolean)
    .join(' ');
  const bodyClasses = [
    styles.body,
    bodyPadding === 'gutter' ? styles.bodyGutter : '',
    bodyClassName,
  ]
    .filter(Boolean)
    .join(' ');

  const viewModeItems: SegmentedTabItem<Mode>[] | null =
    viewMode && !viewMode.hidden
      ? [
          {
            id: (viewMode.tableId ?? ('table' as Mode)),
            label: viewMode.tableLabel,
            title: typeof viewMode.tableLabel === 'string' ? viewMode.tableLabel : undefined,
            ariaLabel:
              typeof viewMode.tableLabel === 'string' ? viewMode.tableLabel : undefined,
            testId: viewMode.tableTestId,
          },
          {
            id: (viewMode.cardsId ?? ('grid' as Mode)),
            label: viewMode.cardsLabel,
            title: typeof viewMode.cardsLabel === 'string' ? viewMode.cardsLabel : undefined,
            ariaLabel:
              typeof viewMode.cardsLabel === 'string' ? viewMode.cardsLabel : undefined,
            testId: viewMode.cardsTestId,
          },
        ]
      : null;

  const renderedBody = loading && loadingFallback
    ? loadingFallback
    : empty && emptyFallback
      ? emptyFallback
      : children;

  const renderedFooter = hasContent(footer)
    ? footer
    : pagination
      ? (
        <PaginationFooter
          variant="flush"
          showPageJump
          {...pagination}
        />
      )
      : null;

  return (
    <Component
      id={id}
      role={role}
      aria-labelledby={ariaLabelledby}
      className={rootClasses}
      data-testid={testId}
    >
      {shouldRenderHeader ? (
        <div className={headerClasses} data-testid={headerTestId}>
          {hasHeaderLeft ? (
            <div className={styles.headerLeft}>
              {headerLeading}
              {hasHeaderMeta ? (
                <div className={styles.headerMeta}>
                  {hasContent(summary) ? <span className={styles.summary}>{summary}</span> : null}
                  {hasContent(hint) ? <small className={styles.hint}>{hint}</small> : null}
                </div>
              ) : null}
              {hasContent(selectionBadge) ? (
                <span className={styles.selectionBadge}>{selectionBadge}</span>
              ) : null}
              {hasContent(selectionTools) ? (
                <div className={styles.selectionTools}>{selectionTools}</div>
              ) : null}
            </div>
          ) : null}
          {hasHeaderRight ? (
            <div className={styles.headerRight}>
              {headerActions}
              {viewMode && viewModeItems ? (
                <SegmentedTabs<Mode>
                  idBase={viewMode.idBase ?? 'workspace-list-view-mode'}
                  variant="joined"
                  roleMode="group"
                  responsiveFullWidth={false}
                  ariaLabel={viewMode.ariaLabel}
                  activeTab={viewMode.value}
                  onChange={viewMode.onChange}
                  items={viewModeItems}
                />
              ) : null}
            </div>
          ) : null}
        </div>
      ) : null}

      <div className={bodyClasses}>
        {hasContent(banner) ? banner : null}
        {renderedBody}
      </div>

      {renderedFooter}
    </Component>
  );
}
