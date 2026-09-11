import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import {
  IconChevronRight,
  IconPlus,
  IconRefreshCw,
  IconSearch,
  IconTrash2,
} from '@/components/ui/icons';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useAuthStore, useNotificationStore } from '@/stores';
import {
  codexClientModelsApi,
  type CodexClientModelsState,
} from '@/services/api/codexClientModels';
import { CodexModelInlineEditor } from './components/CodexModelInlineEditor';
import {
  CODEX_CLIENT_MODEL_FILTERS,
  buildCodexClientModelRows,
  countCodexClientModelRows,
  filterCodexClientModelRows,
  type CodexClientModelFilter,
} from './model/codexClientModelsModel';
import styles from './CodexClientModelsPage.module.scss';

const TABLE_COLUMN_COUNT = 6;

const resolveErrorMessage = (error: unknown, fallback: string): string => {
  if (error instanceof Error && error.message) return error.message;
  if (typeof error === 'string' && error) return error;
  return fallback;
};

export function CodexClientModelsPage() {
  const { t } = useTranslation();
  const { showNotification, showConfirmation } = useNotificationStore();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const supportsInherit = useAuthStore((state) => state.supportsCodexClientModelInherit);
  const disableControls = connectionStatus !== 'connected';

  const [state, setState] = useState<CodexClientModelsState | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [search, setSearch] = useState('');
  const [filter, setFilter] = useState<CodexClientModelFilter>('all');
  const [expandedSlug, setExpandedSlug] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState('');
  const savingRef = useRef(false);

  const load = useCallback(
    async (signal?: AbortSignal) => {
      setLoading(true);
      setError('');
      try {
        const next = await codexClientModelsApi.getState(signal);
        if (signal?.aborted) return;
        setState(next);
      } catch (loadError) {
        if (signal?.aborted) return;
        setError(resolveErrorMessage(loadError, t('codex_client_models.status_load_failed')));
      } finally {
        if (!signal?.aborted) {
          setLoading(false);
        }
      }
    },
    [t]
  );

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load]);

  useHeaderRefresh(() => load(), !disableControls);

  const rows = useMemo(() => (state ? buildCodexClientModelRows(state) : []), [state]);
  const rowsBySlug = useMemo(() => new Map(rows.map((row) => [row.slug, row])), [rows]);
  const counts = useMemo(() => countCodexClientModelRows(rows), [rows]);
  const visibleRows = useMemo(
    () => filterCodexClientModelRows(rows, filter, search),
    [rows, filter, search]
  );
  const catalog = useMemo(() => state?.models ?? [], [state]);
  const overrideDocument = useMemo(() => state?.override ?? {}, [state]);
  const hasOverride = Object.keys(state?.override ?? {}).length > 0;

  const closeEditor = useCallback(() => {
    setSaveError('');
    setExpandedSlug(null);
    setCreating(false);
  }, []);

  const toggleRow = useCallback((slug: string) => {
    setSaveError('');
    setCreating(false);
    setExpandedSlug((previous) => (previous === slug ? null : slug));
  }, []);

  const openCreate = useCallback(() => {
    setSaveError('');
    setExpandedSlug(null);
    setCreating(true);
  }, []);

  const resolveEditorError = useCallback(
    (mutationError: unknown) =>
      resolveErrorMessage(mutationError, t('codex_client_models.save_failed')),
    [t]
  );

  const handleSave = useCallback(
    async (slug: string, patch: Record<string, unknown> | null) => {
      if (savingRef.current) return;
      savingRef.current = true;
      setSaving(true);
      setSaveError('');
      try {
        const next = await codexClientModelsApi.setOverrideEntry(slug, patch);
        setState(next);
        closeEditor();
        showNotification(t('codex_client_models.save_success', { slug }), 'success');
      } catch (mutationError) {
        setSaveError(resolveEditorError(mutationError));
      } finally {
        savingRef.current = false;
        setSaving(false);
      }
    },
    [closeEditor, resolveEditorError, showNotification, t]
  );

  const handleDeleteOverride = useCallback(
    async (slug: string) => {
      try {
        const next = await codexClientModelsApi.deleteOverrideEntry(slug);
        setState(next);
        closeEditor();
        showNotification(t('codex_client_models.delete_success', { slug }), 'success');
      } catch (mutationError) {
        const message = resolveEditorError(mutationError);
        setSaveError(message);
        showNotification(message, 'error');
      }
    },
    [closeEditor, resolveEditorError, showNotification, t]
  );

  const requestDeleteOverride = useCallback(
    (slug: string) => {
      showConfirmation({
        title: t('codex_client_models.delete_confirm_title'),
        message: t('codex_client_models.delete_confirm_message', { slug }),
        confirmText: t('codex_client_models.delete_override'),
        cancelText: t('common.cancel'),
        variant: 'danger',
        onConfirm: async () => {
          await handleDeleteOverride(slug);
        },
      });
    },
    [handleDeleteOverride, showConfirmation, t]
  );

  const handleClearOverride = useCallback(async () => {
    try {
      const next = await codexClientModelsApi.clearOverride();
      setState(next);
      closeEditor();
      showNotification(t('codex_client_models.clear_success'), 'success');
    } catch (mutationError) {
      showNotification(resolveEditorError(mutationError), 'error');
    }
  }, [closeEditor, resolveEditorError, showNotification, t]);

  const requestClearOverride = useCallback(() => {
    showConfirmation({
      title: t('codex_client_models.clear_confirm_title'),
      message: t('codex_client_models.clear_confirm_message'),
      confirmText: t('codex_client_models.clear_all'),
      cancelText: t('common.cancel'),
      variant: 'danger',
      onConfirm: async () => {
        await handleClearOverride();
      },
    });
  }, [handleClearOverride, showConfirmation, t]);

  const isSlugTaken = useCallback((slug: string) => rowsBySlug.has(slug), [rowsBySlug]);

  const formatContextWindow = (value: number | null) =>
    value === null ? '--' : value.toLocaleString();

  const createEditor = creating ? (
    <tr className={styles.editorRow}>
      <td colSpan={TABLE_COLUMN_COUNT}>
        <CodexModelInlineEditor
          key="create"
          mode="create"
          slug=""
          effectiveEntry={null}
          patch={undefined}
          catalog={catalog}
          overrideDocument={overrideDocument}
          supportsInherit={supportsInherit}
          isSlugTaken={isSlugTaken}
          saving={saving}
          serverError={saveError}
          onCancel={closeEditor}
          onSave={handleSave}
          onDelete={requestDeleteOverride}
        />
      </td>
    </tr>
  ) : null;

  return (
    <div className={styles.page}>
      <section className={styles.actionBar}>
        <div className={styles.metaGroup}>
          <span className={styles.metaPill}>
            {t('codex_client_models.source_label')}:{' '}
            {state?.source ? state.source : t('codex_client_models.source_embed')}
          </span>
          <span className={styles.metaPill}>
            {t('codex_client_models.revision_label', { revision: state?.revision ?? 0 })}
          </span>
          <span
            className={styles.metaPill}
            title={state?.overridePath || t('codex_client_models.override_path_missing')}
          >
            {t('codex_client_models.override_path_label')}:{' '}
            {state?.overridePath || t('codex_client_models.override_path_missing')}
          </span>
        </div>
        <div className={styles.actionGroup}>
          <Button
            size="xs"
            variant="secondary"
            disabled={disableControls || loading}
            onClick={() => void load()}
          >
            <IconRefreshCw size={14} />
            {t('codex_client_models.refresh')}
          </Button>
          <Button
            size="xs"
            variant="secondary"
            disabled={disableControls || !state}
            onClick={openCreate}
          >
            <IconPlus size={14} />
            {t('codex_client_models.add')}
          </Button>
          <Button
            size="xs"
            variant="danger"
            disabled={disableControls || !hasOverride}
            onClick={requestClearOverride}
          >
            <IconTrash2 size={14} />
            {t('codex_client_models.clear_all')}
          </Button>
        </div>
      </section>

      {state?.overrideError ? (
        <div className={styles.warning}>
          {t('codex_client_models.override_error', { message: state.overrideError })}
        </div>
      ) : null}
      {state && state.overrideErrors.length > 0 ? (
        <div className={styles.warning}>
          {t('codex_client_models.override_errors_title')}
          <ul className={styles.issueList}>
            {state.overrideErrors.map((issue) => (
              <li key={`${issue.slug}:${issue.path}`}>
                {t('codex_client_models.override_errors_item', {
                  slug: issue.slug,
                  path: issue.path || t('codex_client_models.override_errors_whole_entry'),
                  message: issue.error,
                })}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {error ? <div className="error-box">{error}</div> : null}

      <section className={styles.panel}>
        <div className={styles.toolbar}>
          <div className={styles.searchWrap}>
            <Input
              className={styles.searchInput}
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder={t('codex_client_models.search_placeholder')}
              disabled={disableControls}
              rightElement={<IconSearch size={15} />}
            />
          </div>
          <div className={styles.filterGroup}>
            {CODEX_CLIENT_MODEL_FILTERS.map((item) => (
              <button
                key={item}
                type="button"
                className={`${styles.filterButton} ${filter === item ? styles.filterButtonActive : ''}`}
                onClick={() => setFilter(item)}
              >
                <span>{t(`codex_client_models.filter_${item}`)}</span>
                <strong>{counts[item]}</strong>
              </button>
            ))}
          </div>
        </div>

        {loading && !state ? (
          <div className={styles.emptyState}>{t('common.loading')}</div>
        ) : (
          <div className={styles.tableWrap}>
            <table className={styles.table}>
              <thead>
                <tr>
                  <th>{t('codex_client_models.column_model')}</th>
                  <th>{t('codex_client_models.column_origin')}</th>
                  <th>{t('codex_client_models.column_context_window')}</th>
                  <th>{t('codex_client_models.column_reasoning')}</th>
                  <th>{t('codex_client_models.column_visibility')}</th>
                  <th>{t('common.action')}</th>
                </tr>
              </thead>
              <tbody>
                {createEditor}
                {visibleRows.map((row) => {
                  const expanded = row.slug === expandedSlug;
                  return (
                    <Fragment key={row.slug}>
                      <tr
                        className={[styles.row, expanded ? styles.rowExpanded : '']
                          .filter(Boolean)
                          .join(' ')}
                        onClick={() => toggleRow(row.slug)}
                      >
                        <td className={styles.modelCell}>
                          <button
                            type="button"
                            className={styles.modelToggle}
                            aria-expanded={expanded}
                            disabled={disableControls}
                            onClick={(event) => {
                              event.stopPropagation();
                              toggleRow(row.slug);
                            }}
                          >
                            <IconChevronRight
                              size={14}
                              className={expanded ? styles.chevronOpen : styles.chevron}
                            />
                            <span className={styles.modelName}>
                              <strong>{row.slug}</strong>
                              <small>{row.displayName}</small>
                            </span>
                          </button>
                        </td>
                        <td>
                          <span
                            className={`${styles.originBadge} ${styles[`origin_${row.origin}`]}`}
                          >
                            {t(`codex_client_models.origin_${row.origin}`)}
                          </span>
                        </td>
                        <td>{formatContextWindow(row.contextWindow)}</td>
                        <td>{row.reasoningLevel || '--'}</td>
                        <td>{row.visibility || '--'}</td>
                        <td>
                          <div className={styles.rowActions}>
                            {row.hasOverride ? (
                              <button
                                type="button"
                                className={styles.iconAction}
                                title={t('codex_client_models.delete_override')}
                                aria-label={t('codex_client_models.delete_override')}
                                disabled={disableControls}
                                onClick={(event) => {
                                  event.stopPropagation();
                                  requestDeleteOverride(row.slug);
                                }}
                              >
                                <IconTrash2 size={14} />
                              </button>
                            ) : null}
                          </div>
                        </td>
                      </tr>
                      {expanded ? (
                        <tr className={styles.editorRow}>
                          <td colSpan={TABLE_COLUMN_COUNT}>
                            <CodexModelInlineEditor
                              key={`edit:${row.slug}`}
                              mode="edit"
                              slug={row.slug}
                              effectiveEntry={row.entry}
                              patch={row.patch}
                              catalog={catalog}
                              overrideDocument={overrideDocument}
                              supportsInherit={supportsInherit}
                              isSlugTaken={isSlugTaken}
                              saving={saving}
                              serverError={saveError}
                              onCancel={closeEditor}
                              onSave={handleSave}
                              onDelete={requestDeleteOverride}
                            />
                          </td>
                        </tr>
                      ) : null}
                    </Fragment>
                  );
                })}
              </tbody>
            </table>
            {!creating && visibleRows.length === 0 ? (
              <div className={styles.emptyState}>{t('codex_client_models.empty')}</div>
            ) : null}
          </div>
        )}
      </section>
    </div>
  );
}
