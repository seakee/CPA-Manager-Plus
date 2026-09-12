import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { IconRefreshCw } from '@/components/ui/icons';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import type { CodexSubscriptionEntry } from '@/features/accounts/model/codexSubscription';
import { buildCodexSubscriptionTabFields } from '@/features/accounts/model/codexSubscription';
import { AccountDetailFieldList } from './AccountDetailFieldList';
import styles from '@/features/accounts/AccountsPage.module.scss';

interface AccountSubscriptionTabProps {
  entry: CodexSubscriptionEntry;
  missingAccountId: boolean;
  refreshing: boolean;
  onRefresh: () => void;
}

export function AccountSubscriptionTab({
  entry,
  missingAccountId,
  refreshing,
  onRefresh,
}: AccountSubscriptionTabProps) {
  const { t } = useTranslation();
  const readyFields = buildCodexSubscriptionTabFields(entry);
  const failed = entry.status === 'soft_failed';

  return (
    <div className={styles.quotaTab} data-account-subscription-tab="true">
      <div className={styles.quotaTabHeader}>
        <div className={styles.quotaPageHeading}>
          <h2 className={styles.quotaPageTitle}>{t('accounts.detail_tab_subscription')}</h2>
          <p>{t('accounts.detail_subscription_desc')}</p>
        </div>
        <div className={styles.quotaTabActions}>
          <Button
            variant="secondary"
            size="sm"
            onClick={onRefresh}
            disabled={refreshing || missingAccountId}
            loading={refreshing}
          >
            {!refreshing ? <IconRefreshCw size={15} /> : null}
            {t('accounts.detail_subscription_refresh')}
          </Button>
        </div>
      </div>

      {missingAccountId ? (
        <p className={styles.quotaTabStatus}>{t('accounts.detail_subscription_missing_account')}</p>
      ) : null}

      {entry.status === 'loading' && readyFields.length === 0 ? (
        <div className={styles.quotaTabStatus}>
          <LoadingSpinner size={16} />
          <span>{t('accounts.detail_subscription_loading')}</span>
        </div>
      ) : null}

      {failed ? (
        <p className={styles.quotaTabStatus}>
          {t('accounts.detail_subscription_soft_failed', { kind: entry.errorKind })}
        </p>
      ) : null}

      {readyFields.length > 0 ? <AccountDetailFieldList fields={readyFields} /> : null}

      {!missingAccountId && entry.status === 'idle' ? (
        <p className={styles.quotaTabStatus}>{t('accounts.detail_subscription_empty')}</p>
      ) : null}
    </div>
  );
}
