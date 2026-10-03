import { useCallback, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { DropdownMenu, type DropdownMenuItem } from '@/components/ui/DropdownMenu';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { SegmentedTabs } from '@/components/ui/SegmentedTabs';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { IconCopy, IconMoreVertical, IconRefreshCw } from '@/components/ui/icons';
import type { AccountRow } from '@/features/accounts/model/accountRows';
import {
  buildAccountModelRuleDiff,
  buildAccountModelRuleProjection,
  setAccountModelExactRule,
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
  const [query, setQuery] = useState('');
  const [filter, setFilter] = useState<AccountModelFilter>('all');
  const { state, draft, sharedSourceReadOnly, sourceMemberCount } = editor;
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
      }),
    [
      credentialPrefix,
      credentialRules,
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
  const changedRules = useMemo(
    () => new Set([...diff.added, ...diff.removed].map((rule) => rule.trim().toLowerCase())),
    [diff.added, diff.removed]
  );
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
    const next = setAccountModelExactRule(
      credentialRules,
      model.ruleModelId,
      excluded,
      model.equivalentRuntimeModelIds
    );
    editor.updateField('excludedModelsText', next.join('\n'));
  };

  type RowControl =
    | { kind: 'switch'; checked: boolean; disabled: boolean; title?: string }
    | { kind: 'locked'; reason: string; onOpen?: () => void };

  // One control per row: a switch when this credential's own exact rule decides the
  // model, otherwise a locked "off" state that names (and links to) the rule that does.
  const getRowControl = (model: AccountModelRuleRow): RowControl => {
    if (sharedSourceReadOnly && (model.credentialPatterns.length > 0 || model.scope === 'available')) {
      return { kind: 'locked', reason: t('accounts.model_locked_shared') };
    }
    const unresolvedTitle = !model.ruleModelIdResolved
      ? t('accounts.model_rule_mapping_unavailable')
      : undefined;
    if (model.hasCredentialExactRule) {
      if (model.globalPatterns.length > 0) {
        return { kind: 'locked', reason: t('accounts.model_locked_global'), onOpen: onManageGlobalRules };
      }
      if (model.hasCredentialWildcardRule) {
        return { kind: 'locked', reason: t('accounts.model_locked_advanced'), onOpen: onOpenAdvancedRules };
      }
      return {
        kind: 'switch',
        checked: false,
        disabled: editingDisabled || !model.ruleModelIdResolved || !globalRulesKnown,
        title: unresolvedTitle,
      };
    }
    if (model.hasCredentialWildcardRule) {
      return { kind: 'locked', reason: t('accounts.model_locked_advanced'), onOpen: onOpenAdvancedRules };
    }
    if (model.globalPatterns.length > 0) {
      return { kind: 'locked', reason: t('accounts.model_locked_global'), onOpen: onManageGlobalRules };
    }
    return {
      kind: 'switch',
      checked: true,
      disabled: editingDisabled || !model.ruleModelIdResolved,
      title: unresolvedTitle,
    };
  };

  const isRowChanged = (model: AccountModelRuleRow) =>
    Boolean(model.ruleModelId) && changedRules.has(model.ruleModelId.trim().toLowerCase());

  const setShownRows = (enabled: boolean) => {
    if (editingDisabled) return;
    let next = credentialRules;
    filteredRows.forEach((model) => {
      const control = getRowControl(model);
      if (control.kind !== 'switch' || control.disabled || control.checked === enabled) return;
      next = setAccountModelExactRule(
        next,
        model.ruleModelId,
        !enabled,
        model.equivalentRuntimeModelIds
      );
    });
    if (next !== credentialRules) editor.updateField('excludedModelsText', next.join('\n'));
  };

  const listMenuItems: DropdownMenuItem[] = [
    {
      key: 'turn-on-shown',
      label: t('accounts.model_turn_on_shown', { count: filteredRows.length }),
      onClick: () => setShownRows(true),
      disabled: editingDisabled || filteredRows.length === 0,
    },
    {
      key: 'turn-off-shown',
      label: t('accounts.model_turn_off_shown', { count: filteredRows.length }),
      onClick: () => setShownRows(false),
      disabled: editingDisabled || filteredRows.length === 0,
    },
    { key: 'list-divider', type: 'divider' },
    {
      key: 'refresh-models',
      label: t('accounts.model_refresh_list'),
      icon: <IconRefreshCw size={15} />,
      onClick: onRefresh,
      disabled: loading || refreshing,
    },
    {
      key: 'manage-global-rules',
      label: t('accounts.model_manage_global_rules'),
      onClick: onManageGlobalRules,
    },
  ];

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

      {showLoadFailed || showPartialFailed ? (
        <div className={styles.accountModelsWarning} role="alert">
          <span>{t('accounts.model_load_failed')}</span>
          <Button variant="secondary" size="xs" onClick={onRefresh}>
            {t('common.retry')}
          </Button>
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
        <DropdownMenu
          items={listMenuItems}
          ariaLabel={t('accounts.model_list_actions')}
          triggerTitle={t('accounts.model_list_actions')}
          triggerIcon={<IconMoreVertical size={16} />}
          triggerClassName={styles.accountModelsMenuTrigger}
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
            const control = getRowControl(model);
            const changed = isRowChanged(model);
            return (
              <article
                key={model.id}
                className={`${styles.accountModelRow} ${
                  isKnownDisabledScope(model.scope) ? styles.accountModelRowExcluded : ''
                } ${changed ? styles.accountModelRowChanged : ''}`}
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
                    {changed ? (
                      <span
                        className={styles.accountModelUnsavedDot}
                        role="img"
                        aria-label={t('accounts.config_unsaved')}
                        title={t('accounts.config_unsaved')}
                      />
                    ) : null}
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
                <div className={styles.accountModelActions}>
                  {control.kind === 'locked' ? (
                    control.onOpen ? (
                      <button
                        type="button"
                        className={styles.accountModelLockedReason}
                        onClick={control.onOpen}
                      >
                        {control.reason}
                      </button>
                    ) : (
                      <span className={styles.accountModelLockedReason}>{control.reason}</span>
                    )
                  ) : null}
                  <span title={control.kind === 'switch' ? control.title : control.reason}>
                    <ToggleSwitch
                      checked={control.kind === 'switch' ? control.checked : false}
                      disabled={control.kind === 'locked' || control.disabled}
                      onChange={(enabled) => updateExactRule(model, !enabled)}
                      ariaLabel={t('accounts.model_toggle_label', { model: model.id })}
                    />
                  </span>
                </div>
              </article>
            );
          })}
        </div>
      )}
    </div>
  );
}
