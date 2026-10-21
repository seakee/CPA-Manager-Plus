import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { DropdownMenu } from '@/components/ui/DropdownMenu';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { IconMoreVertical, IconSearch, IconTrash2 } from '@/components/ui/icons';
import type { OAuthConfigLoadState } from '@/features/authFiles/constants';
import styles from './OAuthRuleCards.module.scss';

const COLLAPSED_MODEL_COUNT = 3;

export type OAuthExcludedCardProps = {
  disableControls: boolean;
  loadState: OAuthConfigLoadState;
  hasLoaded?: boolean;
  excluded: Record<string, string[]>;
  onRetry: () => void | Promise<void>;
  onAdd: () => void;
  onEdit: (provider: string) => void;
  onDelete: (provider: string) => void;
};

export function OAuthExcludedCard(props: OAuthExcludedCardProps) {
  const { t } = useTranslation();
  const { disableControls, loadState, excluded, onRetry, onAdd, onEdit, onDelete } = props;
  const hasLoaded = props.hasLoaded ?? Object.keys(excluded).length > 0;
  const writesDisabled = disableControls || loadState !== 'ready';
  const [search, setSearch] = useState('');
  const [expandedProviders, setExpandedProviders] = useState<Set<string>>(() => new Set());
  const normalizedSearch = search.trim().toLowerCase();
  const entries = useMemo(
    () => Object.entries(excluded).sort(([left], [right]) => left.localeCompare(right)),
    [excluded]
  );
  const totalRuleCount = useMemo(
    () => entries.reduce((total, [, models]) => total + (models?.length ?? 0), 0),
    [entries]
  );
  const filteredEntries = useMemo(
    () =>
      entries.filter(([provider, models]) => {
        if (!normalizedSearch) return true;
        return (
          provider.toLowerCase().includes(normalizedSearch) ||
          models?.some((model) => model.toLowerCase().includes(normalizedSearch))
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
      title={t('oauth_excluded.title')}
      extra={
        <Button size="sm" onClick={onAdd} disabled={writesDisabled}>
          {t('oauth_excluded.add')}
        </Button>
      }
    >
      {loadState === 'ready' ? (
        <div className={styles.cardScopeHint}>{t('oauth_excluded.scope_hint')}</div>
      ) : null}
      {hasLoaded && entries.length > 0 ? (
        <div className={styles.ruleToolbar}>
          <div className={styles.ruleSummary} role="status">
            {normalizedSearch
              ? t('oauth_excluded.summary_filtered', {
                  visible: filteredEntries.length,
                  providers: entries.length,
                  count: totalRuleCount,
                })
              : t('oauth_excluded.summary', {
                  providers: entries.length,
                  count: totalRuleCount,
                })}
          </div>
          <div className={styles.ruleSearch}>
            <Input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder={t('oauth_excluded.search_placeholder')}
              aria-label={t('oauth_excluded.search_aria')}
              rightElement={<IconSearch size={15} aria-hidden="true" />}
            />
          </div>
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
          title={t('oauth_excluded.upgrade_required_title')}
          description={t('oauth_excluded.upgrade_required_desc')}
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
      ) : Object.keys(excluded).length === 0 ? (
        <EmptyState title={t('oauth_excluded.list_empty_all')} />
      ) : filteredEntries.length === 0 ? (
        <EmptyState title={t('oauth_excluded.search_empty')} />
      ) : (
        <div className={styles.excludedList}>
          {filteredEntries.map(([provider, models]) => {
            const safeModels = models ?? [];
            const providerMatches = provider.toLowerCase().includes(normalizedSearch);
            const matchedModels =
              normalizedSearch && !providerMatches
                ? safeModels.filter((model) => model.toLowerCase().includes(normalizedSearch))
                : safeModels;
            const expanded = expandedProviders.has(provider);
            const visibleModels = expanded
              ? matchedModels
              : matchedModels.slice(0, COLLAPSED_MODEL_COUNT);
            const hiddenCount = matchedModels.length - visibleModels.length;

            return (
              <div key={provider} className={styles.excludedItem}>
                <div className={styles.excludedInfo}>
                  <div className={styles.ruleItemHeading}>
                    <div className={styles.excludedProvider}>{provider}</div>
                    <div className={styles.ruleCount}>
                      {safeModels.length
                        ? t('oauth_excluded.model_count', { count: safeModels.length })
                        : t('oauth_excluded.no_models')}
                    </div>
                  </div>
                  {matchedModels.length ? (
                    <div className={styles.ruleTokenList}>
                      {visibleModels.map((model, index) => (
                        <code key={`${model}\u0000${index}`} className={styles.ruleToken}>
                          {model}
                        </code>
                      ))}
                      {matchedModels.length > COLLAPSED_MODEL_COUNT ? (
                        <button
                          type="button"
                          className={styles.ruleExpandButton}
                          onClick={() => toggleProvider(provider)}
                          aria-expanded={expanded}
                        >
                          {expanded
                            ? t('oauth_excluded.collapse_models')
                            : t('oauth_excluded.expand_models', { count: hiddenCount })}
                        </button>
                      ) : null}
                    </div>
                  ) : null}
                </div>
                <div className={styles.excludedActions}>
                  <Button
                    variant="secondary"
                    size="xs"
                    onClick={() => onEdit(provider)}
                    disabled={writesDisabled}
                  >
                    {t('common.edit')}
                  </Button>
                  <DropdownMenu
                    items={[
                      {
                        key: 'delete-rule',
                        label: t('oauth_excluded.delete'),
                        icon: <IconTrash2 size={15} />,
                        tone: 'danger',
                        onClick: () => onDelete(provider),
                      },
                    ]}
                    ariaLabel={t('oauth_excluded.actions_aria', { provider })}
                    triggerTitle={t('oauth_excluded.actions_aria', { provider })}
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
