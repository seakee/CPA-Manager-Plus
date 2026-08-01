import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import type { OAuthConfigLoadState } from '@/features/authFiles/constants';
import styles from '@/features/authFiles/AuthFilesPage.module.scss';

export type OAuthExcludedCardProps = {
  disableControls: boolean;
  loadState: OAuthConfigLoadState;
  excluded: Record<string, string[]>;
  onRetry: () => void | Promise<void>;
  onAdd: () => void;
  onEdit: (provider: string) => void;
  onDelete: (provider: string) => void;
};

export function OAuthExcludedCard(props: OAuthExcludedCardProps) {
  const { t } = useTranslation();
  const { disableControls, loadState, excluded, onRetry, onAdd, onEdit, onDelete } = props;
  const writesDisabled = disableControls || loadState !== 'ready';

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
      {loadState === 'unsupported' ? (
        <EmptyState
          title={t('oauth_excluded.upgrade_required_title')}
          description={t('oauth_excluded.upgrade_required_desc')}
        />
      ) : loadState === 'error' ? (
        <EmptyState
          title={t('notification.refresh_failed')}
          action={
            <Button variant="secondary" size="sm" onClick={() => void onRetry()}>
              {t('common.refresh')}
            </Button>
          }
        />
      ) : loadState === 'loading' ? (
        <EmptyState title={t('common.loading')} />
      ) : Object.keys(excluded).length === 0 ? (
        <EmptyState title={t('oauth_excluded.list_empty_all')} />
      ) : (
        <div className={styles.excludedList}>
          {Object.entries(excluded).map(([provider, models]) => (
            <div key={provider} className={styles.excludedItem}>
              <div className={styles.excludedInfo}>
                <div className={styles.excludedProvider}>{provider}</div>
                <div className={styles.excludedModels}>
                  {models?.length
                    ? t('oauth_excluded.model_count', { count: models.length })
                    : t('oauth_excluded.no_models')}
                </div>
                {models?.length ? (
                  <div
                    className={styles.excludedModels}
                    title={models.join(', ')}
                  >
                    {models.slice(0, 3).join(' · ')}
                    {models.length > 3 ? ` · +${models.length - 3}` : ''}
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
                <Button
                  variant="danger"
                  size="xs"
                  onClick={() => onDelete(provider)}
                  disabled={writesDisabled}
                >
                  {t('oauth_excluded.delete')}
                </Button>
              </div>
            </div>
          ))}
        </div>
      )}
    </Card>
  );
}
