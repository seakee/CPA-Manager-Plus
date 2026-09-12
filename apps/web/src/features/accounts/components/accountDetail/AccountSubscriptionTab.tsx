import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { IconRefreshCw } from '@/components/ui/icons';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import type { AccountDetailField } from '@/features/accounts/model/accountDetailViewModel';
import type { CodexSubscriptionEntry } from '@/features/accounts/model/codexSubscription';
import { AccountDetailFieldList } from './AccountDetailFieldList';
import styles from '@/features/accounts/AccountsPage.module.scss';

interface AccountSubscriptionTabProps {
  entry: CodexSubscriptionEntry;
  missingAccountId: boolean;
  refreshing: boolean;
  onRefresh: () => void;
}

const booleanField = (
  key: string,
  labelKey: string,
  value: boolean | null
): AccountDetailField | null => {
  if (value === null) return null;
  return {
    key,
    labelKey,
    value: value ? 'common.yes' : 'common.no',
    valueKind: 'i18n',
  };
};

const billingPeriodLabelKey = (period: string): string => {
  const normalized = period.trim().toLowerCase();
  if (normalized === 'monthly' || normalized === 'month') {
    return 'accounts.detail_subscription_billing_monthly';
  }
  if (normalized === 'yearly' || normalized === 'annual' || normalized === 'year') {
    return 'accounts.detail_subscription_billing_yearly';
  }
  return period;
};

const buildSubscriptionFields = (entry: Extract<CodexSubscriptionEntry, { status: 'ready' }>) => {
  const { record } = entry;
  const extras = record.extras;
  const fields: Array<AccountDetailField | null> = [
    record.planType
      ? {
          key: 'planType',
          labelKey: 'accounts.detail_subscription_plan_type',
          value: record.planType,
        }
      : null,
    record.activeStartMs !== null
      ? {
          key: 'activeStartMs',
          labelKey: 'accounts.detail_subscription_active_start',
          value: record.activeStartMs,
          valueKind: 'quota_reset',
        }
      : null,
    record.activeUntilMs !== null
      ? {
          key: 'activeUntilMs',
          labelKey: 'accounts.detail_subscription_active_until',
          value: record.activeUntilMs,
          valueKind: 'quota_reset',
        }
      : null,
    record.billingPeriod
      ? {
          key: 'billingPeriod',
          labelKey: 'accounts.detail_subscription_billing_period',
          value: billingPeriodLabelKey(record.billingPeriod),
          valueKind: billingPeriodLabelKey(record.billingPeriod).startsWith('accounts.')
            ? 'i18n'
            : 'text',
        }
      : null,
    booleanField('willRenew', 'accounts.detail_subscription_will_renew', record.willRenew),
    extras.seatsInUse !== null || extras.seatsEntitled !== null
      ? {
          key: 'seats',
          labelKey: 'accounts.detail_subscription_seats',
          value:
            extras.seatsInUse !== null && extras.seatsEntitled !== null
              ? `${extras.seatsInUse} / ${extras.seatsEntitled}`
              : String(extras.seatsInUse ?? extras.seatsEntitled),
        }
      : null,
    extras.isDelinquent === true
      ? {
          key: 'isDelinquent',
          labelKey: 'accounts.detail_subscription_delinquent',
          value: 'common.yes',
          valueKind: 'i18n',
        }
      : null,
    extras.gracePeriodEndMs !== null
      ? {
          key: 'gracePeriodEndMs',
          labelKey: 'accounts.detail_subscription_grace_period_end',
          value: extras.gracePeriodEndMs,
          valueKind: 'quota_reset',
        }
      : null,
    extras.discountLabel
      ? {
          key: 'discount',
          labelKey: 'accounts.detail_subscription_discount',
          value: extras.discountLabel,
        }
      : null,
    {
      key: 'fetchedAtMs',
      labelKey: 'accounts.detail_subscription_fetched_at',
      value: record.fetchedAtMs,
      valueKind: 'timestamp',
    },
  ];
  return fields.filter((field): field is AccountDetailField => field !== null);
};

export function AccountSubscriptionTab({
  entry,
  missingAccountId,
  refreshing,
  onRefresh,
}: AccountSubscriptionTabProps) {
  const { t } = useTranslation();
  const readyFields = entry.status === 'ready' ? buildSubscriptionFields(entry) : [];
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
