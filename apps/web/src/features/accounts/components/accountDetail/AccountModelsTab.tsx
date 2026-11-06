import { useCallback, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { SegmentedTabs } from '@/components/ui/SegmentedTabs';
import { IconCopy, IconRefreshCw } from '@/components/ui/icons';
import type { AccountRow } from '@/features/accounts/model/accountRows';
import {
  buildAccountModelRuleDiff,
  buildAccountModelRuleProjection,
  getAccountModelExclusionPriority,
  setEffectiveAccountModelExactRule,
  type AccountModelRuleRow,
  type AccountModelRuleScope,
} from '@/features/accounts/model/accountModelRules';
import {
  getProviderRecordValues,
  normalizeProviderKey,
  parseExcludedModelsText,
  type AuthFileModelItem,
  type OAuthConfigLoadState,
} from '@/features/authFiles/constants';
import type { UseAuthFileConfigurationEditorResult } from '@/features/authFiles/hooks/useAuthFileConfigurationEditor';
import type { OAuthModelAliasEntry } from '@/types';
import { useAuthStore } from '@/stores/useAuthStore';
import styles from '@/features/accounts/AccountsPage.module.scss';

type AccountModelFilter = 'all' | 'available' | 'disabled';

interface AccountModelsTabProps {
  row: AccountRow;
  disableControls: boolean;
  fileName: string;
  fileType: string;
  loading: boolean;
  refreshing: boolean;
  error: 'unsupported' | 'failed' | null;
  models: AuthFileModelItem[];
  modelDefinitions: AuthFileModelItem[];
  modelDefinitionsLoading: boolean;
  modelDefinitionsError: 'unsupported' | 'failed' | null;
  globalExcluded: Record<string, string[]>;
  globalExcludedState: OAuthConfigLoadState;
  aliases: Record<string, OAuthModelAliasEntry[]>;
  editor: UseAuthFileConfigurationEditorResult;
  onRefresh: () => void;
  onManageGlobalRules: () => void;
  onOpenAdvancedRules: () => void;
  onCopyText: (value: string) => void;
}

const getScopeTranslationKey = (scope: AccountModelRuleScope) => {
  switch (scope) {
    case 'unknown':
      return 'accounts.model_scope_unknown';
    case 'credential':
      return 'accounts.model_scope_credential';
    case 'shared':
      return 'accounts.model_scope_shared';
    case 'global':
      return 'accounts.model_scope_global';
    case 'both':
      return 'accounts.model_scope_both';
    case 'shared-global':
      return 'accounts.model_scope_shared_global';
    default:
      return 'accounts.model_scope_available';
  }
};

const getScopeClassName = (scope: AccountModelRuleScope) => {
  switch (scope) {
    case 'unknown':
      return styles.accountModelScopeUnknown;
    case 'credential':
    case 'shared':
      return styles.accountModelScopeCredential;
    case 'global':
      return styles.accountModelScopeGlobal;
    case 'both':
    case 'shared-global':
      return styles.accountModelScopeBoth;
    default:
      return styles.accountModelScopeAvailable;
  }
};

const isKnownDisabledScope = (scope: AccountModelRuleScope): boolean =>
  scope !== 'available' && scope !== 'unknown';

export function AccountModelsTab({
  row,
  disableControls,
  fileName,
  fileType,
  loading,
  refreshing,
  error,
  models,
  modelDefinitions,
  modelDefinitionsLoading,
  modelDefinitionsError,
  globalExcluded,
  globalExcludedState,
  aliases,
  editor,
  onRefresh,
  onManageGlobalRules,
  onOpenAdvancedRules,
  onCopyText,
}: AccountModelsTabProps) {
  const { t } = useTranslation();
  const serverVersion = useAuthStore((state) => state.serverVersion);
  const exclusionPriority = getAccountModelExclusionPriority(serverVersion);
  const [query, setQuery] = useState('');
  const [filter, setFilter] = useState<AccountModelFilter>('all');
  const { state, draft, dirty, canSave, sharedSourceReadOnly, sourceMemberCount } = editor;
  const providerKey = normalizeProviderKey(fileType || row.provider);
  const globalRulesKnown = globalExcludedState === 'ready';
  const aliasEntries = useMemo(() => {
    const seen = new Set<string>();
    return getProviderRecordValues(aliases, providerKey)
      .flat()
      .filter((entry) => {
        const key = `${entry.name.trim().toLowerCase()}\u0000${entry.alias.trim().toLowerCase()}`;
        if (seen.has(key)) return false;
        seen.add(key);
        return true;
      });
  }, [aliases, providerKey]);
  const aliasesByModelId = useMemo(() => {
    const result = new Map<string, string[]>();
    aliasEntries.forEach((entry) => {
      const modelId = entry.name.trim().toLowerCase();
      const alias = entry.alias.trim();
      if (!modelId || !alias) return;
      result.set(modelId, [...(result.get(modelId) ?? []), alias]);
    });
    return result;
  }, [aliasEntries]);
  const credentialRules = useMemo(
    () => parseExcludedModelsText(draft?.excludedModelsText ?? ''),
    [draft?.excludedModelsText]
  );
  const credentialPrefix = state?.originalDraft?.prefix ?? '';
  const projection = useMemo(
    () =>
      buildAccountModelRuleProjection({
        provider: providerKey,
        runtimeModels: models,
        modelDefinitions,
        credentialRules,
        globalRules: globalRulesKnown ? globalExcluded : {},
        globalRulesKnown,
        credentialRulesShared: sharedSourceReadOnly,
        credentialPrefix,
        exclusionPriority,
      }),
    [
      credentialPrefix,
      credentialRules,
      exclusionPriority,
      globalExcluded,
      globalRulesKnown,
      modelDefinitions,
      models,
      providerKey,
      sharedSourceReadOnly,
    ]
  );
  const diff = useMemo(
    () =>
      buildAccountModelRuleDiff(
        state?.originalDraft?.excludedModelsText ?? '',
        draft?.excludedModelsText ?? ''
      ),
    [draft?.excludedModelsText, state?.originalDraft?.excludedModelsText]
  );
  const modelRulesDirty = diff.added.length > 0 || diff.removed.length > 0;
  // Override-capable CPA: a nonempty credential list replaces global rules; empty inherits them.
  const originalCredentialRuleCount = diff.removed.length + diff.unchanged.length;
  const globalInheritanceChange =
    exclusionPriority === 'override' && projection.globalRules.length > 0
      ? originalCredentialRuleCount === 0 && credentialRules.length > 0
        ? 'stops'
        : originalCredentialRuleCount > 0 && credentialRules.length === 0
          ? 'resumes'
          : null
      : null;
  const precedenceUnknown =
    globalRulesKnown && exclusionPriority === 'unknown' && projection.globalRules.length > 0;
  const hasUnresolvedRows = useMemo(
    () => projection.rows.some((row) => !row.ruleModelIdResolved),
    [projection.rows]
  );

  const getModelAliases = useCallback(
    (model: AccountModelRuleRow): string[] => {
      const lookupKey =
        model.ruleModelIdResolved && model.ruleModelId
          ? model.ruleModelId.trim().toLowerCase()
          : model.id.trim().toLowerCase();
      return (
        aliasesByModelId.get(lookupKey) ??
        aliasesByModelId.get(model.id.trim().toLowerCase()) ??
        []
      );
    },
    [aliasesByModelId]
  );

  const filteredRows = useMemo(() => {
    const normalizedQuery = query.trim().toLowerCase();
    return projection.rows.filter((model) => {
      if (filter === 'available' && model.scope !== 'available') return false;
      if (filter === 'disabled' && !isKnownDisabledScope(model.scope)) return false;
      if (!normalizedQuery) return true;
      const modelAliases = getModelAliases(model);
      return [model.id, model.display_name, model.type, ...modelAliases]
        .filter(Boolean)
        .some((value) => String(value).toLowerCase().includes(normalizedQuery));
    });
  }, [filter, getModelAliases, projection.rows, query]);

  const editorReady = Boolean(state && !state.loading && !state.error && draft && state.record);
  const editingDisabled =
    disableControls ||
    row.disabled ||
    row.runtimeOnly ||
    sharedSourceReadOnly ||
    !globalRulesKnown ||
    precedenceUnknown ||
    state?.saving === true ||
    !editorReady;
  const disabledCount = projection.rows.filter((model) => isKnownDisabledScope(model.scope)).length;
  const filterItems = useMemo(
    () => [
      { id: 'all' as const, label: t('accounts.model_filter_all') },
      { id: 'available' as const, label: t('accounts.model_filter_available') },
      {
        id: 'disabled' as const,
        label: t('accounts.model_filter_disabled', { count: disabledCount }),
      },
    ],
    [disabledCount, t]
  );

  const updateExactRule = (model: AccountModelRuleRow, excluded: boolean) => {
    if (editingDisabled || !model.ruleModelIdResolved) return;
    const next = setEffectiveAccountModelExactRule({
      credentialRules,
      globalRules: projection.globalRules,
      exclusionPriority,
      modelId: model.ruleModelId,
      excluded,
      equivalentExactModelIds: model.equivalentRuntimeModelIds,
    });
    if (next === null) return;
    editor.updateField('excludedModelsText', next.join('\n'));
  };

  const renderRowAction = (model: AccountModelRuleRow) => {
    if (sharedSourceReadOnly && model.credentialPatterns.length > 0) {
      return (
        <Button variant="secondary" size="xs" disabled>
          {t('accounts.model_shared_source_read_only_action')}
        </Button>
      );
    }
    if (model.hasCredentialExactRule) {
      // An emptied credential list would inherit a global rule that still excludes this model.
      const restoredRules = setEffectiveAccountModelExactRule({
        credentialRules,
        globalRules: projection.globalRules,
        exclusionPriority,
        modelId: model.ruleModelId,
        excluded: false,
        equivalentExactModelIds: model.equivalentRuntimeModelIds,
      });
      if (restoredRules === null) {
        return (
          <Button variant="secondary" size="xs" onClick={onManageGlobalRules}>
            {t('accounts.model_manage_global_rules')}
          </Button>
        );
      }
      const remainsExcluded =
        model.hasCredentialWildcardRule ||
        (exclusionPriority !== 'override' && model.globalPatterns.length > 0) ||
        !globalRulesKnown;
      return (
        <Button
          variant="secondary"
          size="xs"
          disabled={editingDisabled || !model.ruleModelIdResolved}
          onClick={() => updateExactRule(model, false)}
          title={
            remainsExcluded
              ? t('accounts.model_remove_exact_rule_hint')
              : t('accounts.model_restore_for_credential')
          }
        >
          {remainsExcluded
            ? t('accounts.model_remove_exact_rule')
            : t('accounts.model_restore_for_credential')}
        </Button>
      );
    }
    if (model.hasCredentialWildcardRule) {
      return (
        <Button
          variant="secondary"
          size="xs"
          disabled={editingDisabled}
          onClick={onOpenAdvancedRules}
        >
          {t('accounts.model_edit_advanced_rules')}
        </Button>
      );
    }
    if (model.globalPatterns.length > 0) {
      return (
        <Button variant="secondary" size="xs" onClick={onManageGlobalRules}>
          {t('accounts.model_manage_global_rules')}
        </Button>
      );
    }
    if (sharedSourceReadOnly) {
      return (
        <Button variant="secondary" size="xs" disabled>
          {t('accounts.model_shared_source_read_only_action')}
        </Button>
      );
    }
    return (
      <Button
        variant="secondary"
        size="xs"
        disabled={editingDisabled || !model.ruleModelIdResolved}
        onClick={() => updateExactRule(model, true)}
        title={
          !model.ruleModelIdResolved
            ? t('accounts.model_rule_mapping_unavailable')
            : undefined
        }
      >
        {t('accounts.model_disable_for_credential')}
      </Button>
    );
  };

  const showUnsupported = error === 'unsupported' && projection.rows.length === 0;
  const showLoadFailed = error === 'failed' && projection.rows.length === 0;
  const showPartialFailed = error === 'failed' && projection.rows.length > 0;
  const showPartialUnsupported = error === 'unsupported' && projection.rows.length > 0;
  const showEmpty =
    !loading &&
    !showUnsupported &&
    !showLoadFailed &&
    projection.rows.length === 0 &&
    projection.advancedCredentialRules.length === 0 &&
    projection.advancedGlobalRules.length === 0;

  return (
    <div
      className={styles.accountModelsStack}
      role="region"
      aria-label={t('accounts.detail_tab_models')}
      aria-busy={loading || modelDefinitionsLoading || globalExcludedState === 'loading'}
    >
      <div className={styles.accountModelsHeader}>
        <div className={styles.accountModelsSummary}>
          <strong>
            {t('accounts.detail_models_summary', { count: projection.rows.length, file: fileName })}
          </strong>
        </div>
        <div className={styles.headerActions}>
          <Button variant="secondary" size="sm" onClick={onManageGlobalRules}>
            {t('accounts.model_manage_global_rules')}
          </Button>
          <Button
            variant="secondary"
            size="sm"
            onClick={onRefresh}
            disabled={loading || refreshing}
            loading={refreshing}
          >
            {!refreshing ? <IconRefreshCw size={14} /> : null}
            {t('common.refresh')}
          </Button>
        </div>
      </div>

      {row.runtimeOnly ? (
        <div className={styles.configurationReadOnlyNotice} role="note">
          {t('accounts.config_runtime_only_desc')}
        </div>
      ) : sharedSourceReadOnly ? (
        <div className={styles.configurationReadOnlyNotice} role="note">
          {t('accounts.config_shared_source_read_only', { count: sourceMemberCount })}
        </div>
      ) : row.disabled ? (
        <div className={styles.configurationReadOnlyNotice} role="note">
          {t('accounts.config_disabled_read_only')}
        </div>
      ) : !state || state.loading ? (
        <div className={styles.accountModelsInlineStatus} role="status">
          <span>{t('accounts.config_loading')}</span>
        </div>
      ) : state?.error ? (
        <div className={styles.accountModelsWarning} role="alert">
          <span>{t('accounts.model_config_unavailable')}</span>
          <Button variant="secondary" size="xs" onClick={() => void editor.reload()}>
            {t('common.retry')}
          </Button>
        </div>
      ) : null}

      {globalExcludedState === 'loading' ? (
        <div className={styles.accountModelsInlineStatus} role="status">
          <span>{t('accounts.model_global_rules_loading')}</span>
        </div>
      ) : globalExcludedState === 'error' || globalExcludedState === 'unsupported' ? (
        <div className={styles.accountModelsWarning} role="note">
          {t('accounts.model_global_rules_unavailable')}
        </div>
      ) : null}

      {modelDefinitionsError === 'failed' ? (
        <div className={styles.accountModelsWarning} role="note">
          {t('accounts.model_definitions_partial')}
        </div>
      ) : null}

      {showPartialUnsupported ? (
        <div className={styles.accountModelsWarning} role="note">
          {t('accounts.model_runtime_list_unsupported')}
        </div>
      ) : null}

      {hasUnresolvedRows && !modelDefinitionsLoading ? (
        <div className={styles.accountModelsWarning} role="note">
          {t('accounts.model_rule_mapping_unavailable')}
        </div>
      ) : null}

      {precedenceUnknown ? (
        <div className={styles.accountModelsWarning} role="note">
          {t('accounts.model_precedence_unknown', { count: projection.globalRules.length })}
        </div>
      ) : null}

      {globalInheritanceChange && !sharedSourceReadOnly ? (
        <div className={styles.accountModelsWarning} role="note">
          {t(
            globalInheritanceChange === 'stops'
              ? 'accounts.model_change_stops_inheriting_global'
              : 'accounts.model_change_resumes_inheriting_global',
            { count: projection.globalRules.length }
          )}
        </div>
      ) : null}

      {showLoadFailed || showPartialFailed ? (
        <div className={styles.accountModelsWarning} role="alert">
          <span>{t('accounts.model_load_failed')}</span>
          <Button variant="secondary" size="xs" onClick={onRefresh}>
            {t('common.retry')}
          </Button>
        </div>
      ) : null}

      {editorReady && !sharedSourceReadOnly ? (
        <div className={styles.configurationToolbar}>
          <div className={styles.accountModelsChangeSummary} aria-live="polite">
            {dirty ? (
              <>
                <span className={styles.configurationDirtyBadge}>
                  {t('accounts.config_unsaved')}
                </span>
                {modelRulesDirty ? (
                  <span>
                    {t('accounts.model_change_summary', {
                      added: diff.added.length,
                      removed: diff.removed.length,
                      unchanged: diff.unchanged.length,
                    })}
                  </span>
                ) : (
                  <span>{t('accounts.model_other_config_changes')}</span>
                )}
              </>
            ) : (
              <span>{t('accounts.model_changes_empty')}</span>
            )}
          </div>
          <div className={styles.configurationToolbarActions}>
            <Button
              variant="secondary"
              size="sm"
              onClick={editor.reset}
              disabled={!dirty || state?.saving}
            >
              {t('common.reset')}
            </Button>
            <Button
              size="sm"
              onClick={() => void editor.save()}
              loading={state?.saving}
              disabled={!canSave || row.disabled}
            >
              {t('common.save')}
            </Button>
          </div>
        </div>
      ) : null}

      <div className={styles.accountModelsControls}>
        <Input
          value={query}
          placeholder={t('accounts.model_search_placeholder')}
          aria-label={t('accounts.model_search_label')}
          onChange={(event) => setQuery(event.target.value)}
        />
        <SegmentedTabs<AccountModelFilter>
          items={filterItems}
          activeTab={filter}
          onChange={setFilter}
          ariaLabel={t('accounts.model_filter_label')}
          idBase="account-model-filter"
          equalWidth
          fullWidth
        />
      </div>

      {projection.advancedCredentialRules.length > 0 ||
      projection.advancedGlobalRules.length > 0 ? (
        <div className={styles.accountModelsRuleStrip}>
          {projection.advancedCredentialRules.map((rule) => (
            <button
              key={`credential:${rule}`}
              type="button"
              className={`${styles.accountModelsRuleChip} ${styles.accountModelsRuleChipCredential}`}
              onClick={onOpenAdvancedRules}
              title={t('accounts.model_edit_advanced_rules')}
            >
              {rule}
            </button>
          ))}
          {projection.advancedGlobalRules.map((rule) => (
            <button
              key={`global:${rule}`}
              type="button"
              className={`${styles.accountModelsRuleChip} ${styles.accountModelsRuleChipGlobal}`}
              onClick={onManageGlobalRules}
              title={t('accounts.model_manage_global_rules')}
            >
              {rule}
            </button>
          ))}
        </div>
      ) : null}

      {loading && projection.rows.length === 0 ? (
        <div className={styles.configurationLoading}>
          <span>{t('auth_files.models_loading')}</span>
        </div>
      ) : showUnsupported ? (
        <EmptyState
          title={t('auth_files.models_unsupported')}
          description={t('auth_files.models_unsupported_desc')}
        />
      ) : showLoadFailed ? null : showEmpty ? (
        <EmptyState
          title={t('auth_files.models_empty')}
          description={t('auth_files.models_empty_desc')}
        />
      ) : filteredRows.length === 0 ? (
        <EmptyState title={t('accounts.model_filter_empty')} />
      ) : (
        <div className={styles.accountModelsList}>
          {filteredRows.map((model) => {
            const modelAliases = getModelAliases(model);
            return (
              <article
                key={model.id}
                className={`${styles.accountModelRow} ${
                  isKnownDisabledScope(model.scope) ? styles.accountModelRowExcluded : ''
                }`}
                data-model-scope={model.scope}
              >
                <div className={styles.accountModelMain}>
                  <div className={styles.accountModelIdentity}>
                    <button
                      type="button"
                      className={styles.accountModelIdButton}
                      onClick={() => onCopyText(model.id)}
                      title={t('common.copy')}
                    >
                      <span>{model.id}</span>
                      <IconCopy size={13} />
                    </button>
                    <span
                      className={`${styles.accountModelScopeBadge} ${getScopeClassName(model.scope)}`}
                    >
                      {t(getScopeTranslationKey(model.scope))}
                    </span>
                  </div>
                  {model.display_name && model.display_name !== model.id ? (
                    <span className={styles.accountModelDisplayName}>{model.display_name}</span>
                  ) : null}
                  <div className={styles.accountModelMeta}>
                    {model.type ? <span>{model.type}</span> : null}
                    {modelAliases.length > 0 ? <span>→ {modelAliases.join(', ')}</span> : null}
                    {model.credentialPatterns.length > 0 ? (
                      <span title={model.credentialPatterns.join(', ')}>
                        {t(
                          sharedSourceReadOnly
                            ? 'accounts.model_rule_count_shared'
                            : 'accounts.model_rule_count_credential',
                          {
                            count: model.credentialPatterns.length,
                          }
                        )}
                      </span>
                    ) : null}
                    {model.globalPatterns.length > 0 ? (
                      <span title={model.globalPatterns.join(', ')}>
                        {t('accounts.model_rule_count_global', {
                          count: model.globalPatterns.length,
                        })}
                      </span>
                    ) : null}
                  </div>
                </div>
                <div className={styles.accountModelActions}>{renderRowAction(model)}</div>
              </article>
            );
          })}
        </div>
      )}
    </div>
  );
}
