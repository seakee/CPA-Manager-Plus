import { useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { DropdownMenu } from '@/components/ui/DropdownMenu';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { ModelMappingDiagram, type ModelMappingDiagramRef } from '@/components/modelAlias';
import { IconChevronUp, IconMoreVertical, IconSearch, IconTrash2 } from '@/components/ui/icons';
import type { OAuthModelAliasEntry } from '@/types';
import type { AuthFileModelItem, OAuthConfigLoadState } from '@/features/authFiles/constants';
import styles from './OAuthRuleCards.module.scss';
type ViewMode = 'diagram' | 'list';

const COLLAPSED_MAPPING_COUNT = 3;

export type OAuthModelAliasCardProps = {
  disableControls: boolean;
  viewMode: ViewMode;
  onViewModeChange: (mode: ViewMode) => void;
  onRetry: () => void | Promise<void>;
  onAdd: () => void;
  onEditProvider: (provider?: string) => void;
  onDeleteProvider: (provider: string) => void;
  loadState: OAuthConfigLoadState;
  hasLoaded?: boolean;
  modelAlias: Record<string, OAuthModelAliasEntry[]>;
  allProviderModels: Record<string, AuthFileModelItem[]>;
  onUpdate: (provider: string, sourceModel: string, newAlias: string) => Promise<void>;
  onDeleteLink: (provider: string, sourceModel: string, alias: string) => void;
  onToggleFork: (
    provider: string,
    sourceModel: string,
    alias: string,
    fork: boolean
  ) => Promise<void>;
  onRenameAlias: (oldAlias: string, newAlias: string) => Promise<void>;
  onDeleteAlias: (aliasName: string) => void;
};

export function OAuthModelAliasCard(props: OAuthModelAliasCardProps) {
  const { t } = useTranslation();
  const diagramRef = useRef<ModelMappingDiagramRef | null>(null);
  const [search, setSearch] = useState('');
  const [expandedProviders, setExpandedProviders] = useState<Set<string>>(() => new Set());
  const {
    disableControls,
    viewMode,
    onViewModeChange,
    onRetry,
    onAdd,
    onEditProvider,
    onDeleteProvider,
    loadState,
    modelAlias,
    allProviderModels,
    onUpdate,
    onDeleteLink,
    onToggleFork,
    onRenameAlias,
    onDeleteAlias,
  } = props;
  const writesDisabled = disableControls || loadState !== 'ready';
  const hasLoaded = props.hasLoaded ?? Object.keys(modelAlias).length > 0;
  const viewModeDisabled = loadState !== 'ready';
  const normalizedSearch = search.trim().toLowerCase();
  const entries = useMemo(
    () => Object.entries(modelAlias).sort(([left], [right]) => left.localeCompare(right)),
    [modelAlias]
  );
  const totalRuleCount = useMemo(
    () => entries.reduce((total, [, mappings]) => total + (mappings?.length ?? 0), 0),
    [entries]
  );
  const filteredEntries = useMemo(
    () =>
      entries.filter(([provider, mappings]) => {
        if (!normalizedSearch) return true;
        return (
          provider.toLowerCase().includes(normalizedSearch) ||
          mappings?.some((entry) =>
            [entry.name, entry.alias, entry.displayName]
              .filter(Boolean)
              .some((value) => String(value).toLowerCase().includes(normalizedSearch))
          )
        );
      }),
    [entries, normalizedSearch]
  );

  const toggleProvider = (provider: string) => {
    setExpandedProviders((current) => {
      const next = new Set(current);
      if (next.has(provider)) next.delete(provider);
      else next.add(provider);
      return next;
    });
  };

  return (
    <Card
      title={t('oauth_model_alias.title')}
      extra={
        <div className={styles.cardExtraButtons}>
          <div className={styles.viewModeSwitch}>
            <Button
              variant={viewMode === 'list' ? 'secondary' : 'ghost'}
              size="sm"
              onClick={() => onViewModeChange('list')}
              disabled={viewModeDisabled}
            >
              {t('oauth_model_alias.view_mode_list')}
            </Button>
            <Button
              variant={viewMode === 'diagram' ? 'secondary' : 'ghost'}
              size="sm"
              onClick={() => onViewModeChange('diagram')}
              disabled={viewModeDisabled}
            >
              {t('oauth_model_alias.view_mode_diagram')}
            </Button>
          </div>
          <Button size="sm" onClick={onAdd} disabled={writesDisabled}>
            {t('oauth_model_alias.add')}
          </Button>
        </div>
      }
    >
      {loadState === 'ready' ? (
        <div className={styles.cardScopeHint}>{t('oauth_model_alias.scope_hint')}</div>
      ) : null}
      {hasLoaded && entries.length > 0 ? (
        <div className={styles.ruleToolbar}>
          <div className={styles.ruleSummary} role="status">
            {viewMode === 'list' && normalizedSearch
              ? t('oauth_model_alias.summary_filtered', {
                  visible: filteredEntries.length,
                  providers: entries.length,
                  count: totalRuleCount,
                })
              : t('oauth_model_alias.summary', {
                  providers: entries.length,
                  count: totalRuleCount,
                })}
          </div>
          {viewMode === 'list' ? (
            <div className={styles.ruleSearch}>
              <Input
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder={t('oauth_model_alias.search_placeholder')}
                aria-label={t('oauth_model_alias.search_aria')}
                rightElement={<IconSearch size={15} aria-hidden="true" />}
              />
            </div>
          ) : null}
        </div>
      ) : null}
      {loadState === 'error' && hasLoaded ? (
        <div role="alert">
          {t('notification.refresh_failed')}{' '}
          <Button variant="secondary" size="sm" onClick={() => void onRetry()}>
            {t('common.retry')}
          </Button>
        </div>
      ) : null}
      {loadState === 'unsupported' ? (
        <EmptyState
          title={t('oauth_model_alias.upgrade_required_title')}
          description={t('oauth_model_alias.upgrade_required_desc')}
        />
      ) : loadState === 'error' && !hasLoaded ? (
        <EmptyState
          title={t('notification.refresh_failed')}
          action={
            <Button variant="secondary" size="sm" onClick={() => void onRetry()}>
              {t('common.refresh')}
            </Button>
          }
        />
      ) : loadState === 'loading' && !hasLoaded ? (
        <EmptyState title={t('common.loading')} />
      ) : viewMode === 'diagram' ? (
        Object.keys(modelAlias).length === 0 ? (
          <EmptyState title={t('oauth_model_alias.list_empty_all')} />
        ) : (
          <div className={styles.aliasChartSection}>
            <div className={styles.aliasChartHeader}>
              <h4 className={styles.aliasChartTitle}>{t('oauth_model_alias.chart_title')}</h4>
              <Button
                variant="ghost"
                size="xs"
                iconOnly
                onClick={() => diagramRef.current?.collapseAll()}
                disabled={writesDisabled}
                title={t('oauth_model_alias.diagram_collapse')}
                aria-label={t('oauth_model_alias.diagram_collapse')}
              >
                <IconChevronUp size={16} />
              </Button>
            </div>
            <ModelMappingDiagram
              ref={diagramRef}
              modelAlias={modelAlias}
              allProviderModels={allProviderModels}
              onUpdate={onUpdate}
              onDeleteLink={onDeleteLink}
              onToggleFork={onToggleFork}
              onRenameAlias={onRenameAlias}
              onDeleteAlias={onDeleteAlias}
              onEditProvider={onEditProvider}
              onDeleteProvider={onDeleteProvider}
              className={styles.aliasChart}
            />
          </div>
        )
      ) : Object.keys(modelAlias).length === 0 ? (
        <EmptyState title={t('oauth_model_alias.list_empty_all')} />
      ) : filteredEntries.length === 0 ? (
        <EmptyState title={t('oauth_model_alias.search_empty')} />
      ) : (
        <div className={styles.excludedList}>
          {filteredEntries.map(([provider, mappings]) => {
            const safeMappings = mappings ?? [];
            const providerMatches = provider.toLowerCase().includes(normalizedSearch);
            const matchedMappings =
              normalizedSearch && !providerMatches
                ? safeMappings.filter((entry) =>
                    [entry.name, entry.alias, entry.displayName]
                      .filter(Boolean)
                      .some((value) => String(value).toLowerCase().includes(normalizedSearch))
                  )
                : safeMappings;
            const expanded = expandedProviders.has(provider);
            const visibleMappings = expanded
              ? matchedMappings
              : matchedMappings.slice(0, COLLAPSED_MAPPING_COUNT);
            const hiddenCount = matchedMappings.length - visibleMappings.length;

            return (
              <div key={provider} className={styles.excludedItem}>
                <div className={styles.excludedInfo}>
                  <div className={styles.ruleItemHeading}>
                    <div className={styles.excludedProvider}>{provider}</div>
                    <div className={styles.ruleCount}>
                      {safeMappings.length
                        ? t('oauth_model_alias.model_count', { count: safeMappings.length })
                        : t('oauth_model_alias.no_models')}
                    </div>
                  </div>
                  {matchedMappings.length ? (
                    <div className={styles.aliasRuleList}>
                      {visibleMappings.map((entry, index) => (
                        <div
                          key={`${entry.name}\u0000${entry.alias}\u0000${index}`}
                          className={styles.aliasRuleItem}
                        >
                          <div className={styles.aliasRuleRoute}>
                            <code title={entry.name}>{entry.name}</code>
                            <span aria-hidden="true">→</span>
                            <code title={entry.alias}>{entry.alias}</code>
                          </div>
                          <div className={styles.aliasRuleBadges}>
                            <span
                              className={
                                entry.fork
                                  ? styles.ruleSemanticBadge
                                  : styles.ruleSemanticBadgeMuted
                              }
                            >
                              {t(
                                entry.fork
                                  ? 'oauth_model_alias.list_fork_keep'
                                  : 'oauth_model_alias.list_fork_replace'
                              )}
                            </span>
                            <span
                              className={
                                entry.forceMapping
                                  ? styles.ruleSemanticBadge
                                  : styles.ruleSemanticBadgeMuted
                              }
                            >
                              {t(
                                entry.forceMapping
                                  ? 'oauth_model_alias.list_response_rewrite'
                                  : 'oauth_model_alias.list_response_passthrough'
                              )}
                            </span>
                            {entry.displayName ? (
                              <span className={styles.ruleSemanticBadge}>
                                {t('oauth_model_alias.list_display_name', {
                                  name: entry.displayName,
                                })}
                              </span>
                            ) : null}
                          </div>
                        </div>
                      ))}
                      {matchedMappings.length > COLLAPSED_MAPPING_COUNT ? (
                        <button
                          type="button"
                          className={styles.ruleExpandButton}
                          onClick={() => toggleProvider(provider)}
                          aria-expanded={expanded}
                        >
                          {expanded
                            ? t('oauth_model_alias.collapse_mappings')
                            : t('oauth_model_alias.expand_mappings', { count: hiddenCount })}
                        </button>
                      ) : null}
                    </div>
                  ) : null}
                </div>
                <div className={styles.excludedActions}>
                  <Button
                    variant="secondary"
                    size="xs"
                    onClick={() => onEditProvider(provider)}
                    disabled={writesDisabled}
                  >
                    {t('common.edit')}
                  </Button>
                  <DropdownMenu
                    items={[
                      {
                        key: 'delete-rule',
                        label: t('oauth_model_alias.delete'),
                        icon: <IconTrash2 size={15} />,
                        tone: 'danger',
                        onClick: () => onDeleteProvider(provider),
                      },
                    ]}
                    ariaLabel={t('oauth_model_alias.actions_aria', { provider })}
                    triggerTitle={t('oauth_model_alias.actions_aria', { provider })}
                    triggerIcon={<IconMoreVertical size={16} />}
                    triggerClassName={styles.ruleMoreButton}
                    disabled={writesDisabled}
                  />
                </div>
              </div>
            );
          })}
        </div>
      )}
    </Card>
  );
}
