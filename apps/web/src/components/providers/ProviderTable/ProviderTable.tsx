import type { KeyboardEvent, MouseEvent, ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { IconCheck, IconEye, IconPencil, IconTrash2, IconX } from '@/components/ui/icons';
import { ProviderStatusBar } from '../ProviderStatusBar';
import { getProviderKindIcon, PROVIDER_KIND_LABELS } from './kindMeta';
import type { ProviderRow } from './rowData';
import { useResizableColumns } from './useResizableColumns';
import styles from './ProviderTable.module.scss';

interface ProviderTableProps {
  rows: ProviderRow[];
  loading: boolean;
  actionsDisabled: boolean;
  toggleDisabled: boolean;
  resolvedTheme: string;
  emptyState: ReactNode;
  onShowDetail: (row: ProviderRow) => void;
  onEdit: (row: ProviderRow) => void;
  onDelete: (row: ProviderRow) => void;
  onToggle: (row: ProviderRow, enabled: boolean) => void;
}

const stopPropagation = (event: MouseEvent) => {
  event.stopPropagation();
};

export function ProviderTable({
  rows,
  loading,
  actionsDisabled,
  toggleDisabled,
  resolvedTheme,
  emptyState,
  onShowDetail,
  onEdit,
  onDelete,
  onToggle,
}: ProviderTableProps) {
  const { t } = useTranslation();
  const { gridTemplateColumns, onResizeStart, resetColumn, isResizable } = useResizableColumns();
  const gridStyle = { gridTemplateColumns } as const;

  if (loading && rows.length === 0) {
    return <div className="hint">{t('common.loading')}</div>;
  }

  if (rows.length === 0) {
    return <>{emptyState}</>;
  }

  const handleRowKeyDown = (event: KeyboardEvent<HTMLDivElement>, row: ProviderRow) => {
    if (event.target !== event.currentTarget) return;
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      onShowDetail(row);
    }
  };

  return (
    <div className={styles.table} role="table" aria-label={t('ai_providers.table_aria_label')}>
      <div className={styles.headerRow} role="row" style={gridStyle}>
        <span role="columnheader">{t('ai_providers.table_col_type')}</span>
        {([
          { label: t('ai_providers.table_col_identity'), colIndex: 1, className: undefined },
          { label: t('common.base_url'), colIndex: 2, className: undefined },
        ] as const).map(({ label, colIndex, className }) => (
          <div key={colIndex} role="columnheader" className={`${styles.resizableHeader} ${className ?? ''}`}>
            {label}
            {isResizable(colIndex) && (
              <div
                className={styles.resizeHandle}
                onPointerDown={(e) => onResizeStart(colIndex, e)}
                onDoubleClick={() => resetColumn(colIndex)}
                role="separator"
                aria-orientation="vertical"
                aria-label={t('common.resize_column')}
              />
            )}
          </div>
        ))}
        <span role="columnheader" className={styles.cellNumeric}>
          {t('ai_providers.table_col_models')}
        </span>
        <span role="columnheader" className={styles.cellNumeric}>
          {t('common.priority')}
        </span>
        <span role="columnheader" className={styles.cellRecentHeader}>
          {t('ai_providers.table_col_recent')}
        </span>
        <span role="columnheader">{t('ai_providers.config_toggle_label')}</span>
        <span role="columnheader" className={styles.cellActionsHeader}>
          {t('ai_providers.table_col_actions')}
        </span>
      </div>

      {rows.map((row) => {
        const kindLabel = PROVIDER_KIND_LABELS[row.kind];
        return (
          <div
            key={row.key}
            role="row"
            tabIndex={0}
            className={`${styles.row} ${row.enabled ? '' : styles.rowDisabled}`}
            style={gridStyle}
            onClick={() => onShowDetail(row)}
            onKeyDown={(event) => handleRowKeyDown(event, row)}
            aria-label={`${kindLabel} ${row.label}`}
          >
            <div className={styles.cellType} role="cell">
              <span className={styles.kindIconFrame} aria-hidden="true">
                <img
                  src={getProviderKindIcon(row.kind, resolvedTheme)}
                  alt=""
                  className={`${styles.kindIcon} ${
                    row.kind === 'codex'
                      ? styles.kindIconCodex
                      : row.kind === 'openai'
                        ? styles.kindIconOpenai
                        : ''
                  }`}
                />
              </span>
              <span className={styles.kindLabel}>{kindLabel}</span>
            </div>

            <div className={styles.cellIdentity} role="cell">
              <div className={styles.identityMain}>
                <span className={styles.identityLabel} title={row.label}>
                  {row.label}
                </span>
                {row.kind === 'openai' && (
                  <span className={styles.identityMeta}>
                    {t('ai_providers.openai_keys_count')}: {row.keyCount}
                  </span>
                )}
              </div>
            </div>

            <div className={styles.cellUrl} role="cell" title={row.baseUrl || undefined}>
              {row.baseUrl || '—'}
            </div>

            <div className={`${styles.cellModels} ${styles.cellNumeric}`} role="cell">
              <span className={styles.cellCaption}>{t('ai_providers.table_col_models')}</span>
              {row.modelCount}
            </div>

            <div className={`${styles.cellPriority} ${styles.cellNumeric}`} role="cell">
              <span className={styles.cellCaption}>{t('common.priority')}</span>
              {row.priority ?? '—'}
            </div>

            <div className={styles.cellRecent} role="cell">
              {row.stats.success + row.stats.failure > 0 ? (
                <>
                  <span className={styles.statSuccess} title={t('stats.success')}>
                    <IconCheck size={12} /> {row.stats.success}
                  </span>
                  <span className={styles.statFailure} title={t('stats.failure')}>
                    <IconX size={12} /> {row.stats.failure}
                  </span>
                  <div className={styles.recentBarWrap}>
                    <ProviderStatusBar statusData={row.statusData} />
                  </div>
                </>
              ) : (
                <span className={styles.noRecent}>{t('status_bar.no_requests')}</span>
              )}
            </div>

            <div className={styles.cellToggle} role="cell" onClick={stopPropagation}>
              <ToggleSwitch
                ariaLabel={t('ai_providers.config_toggle_label')}
                checked={row.enabled}
                disabled={toggleDisabled}
                onChange={(value) => onToggle(row, value)}
              />
            </div>

            <div className={styles.cellActions} role="cell" onClick={stopPropagation}>
              <Button
                variant="secondary"
                size="xs"
                iconOnly
                onClick={() => onEdit(row)}
                disabled={actionsDisabled}
                aria-label={t('common.edit')}
                title={t('common.edit')}
              >
                <IconPencil size={14} />
              </Button>
              <Button
                variant="danger"
                size="xs"
                iconOnly
                onClick={() => onDelete(row)}
                disabled={actionsDisabled}
                aria-label={t('common.delete')}
                title={t('common.delete')}
              >
                <IconTrash2 size={14} />
              </Button>
              <Button
                variant="secondary"
                size="xs"
                iconOnly
                onClick={() => onShowDetail(row)}
                disabled={actionsDisabled}
                aria-label={t('ai_providers.detail_button')}
                title={t('ai_providers.detail_button')}
              >
                <IconEye size={14} />
              </Button>
            </div>
          </div>
        );
      })}
    </div>
  );
}
