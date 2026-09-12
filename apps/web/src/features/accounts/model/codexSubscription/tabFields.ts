import type { AccountDetailField } from '@/features/accounts/model/accountDetailViewModel';
import type { CodexSubscriptionEntry, CodexSubscriptionRecord } from './types';

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

export const billingPeriodLabelKey = (period: string): string => {
  const normalized = period.trim().toLowerCase();
  if (normalized === 'monthly' || normalized === 'month') {
    return 'accounts.detail_subscription_billing_monthly';
  }
  if (normalized === 'yearly' || normalized === 'annual' || normalized === 'year') {
    return 'accounts.detail_subscription_billing_yearly';
  }
  return period;
};

export const buildCodexSubscriptionDetailFields = (
  record: CodexSubscriptionRecord
): AccountDetailField[] => {
  const extras = record.extras;
  const billingLabel = record.billingPeriod ? billingPeriodLabelKey(record.billingPeriod) : null;
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
    billingLabel
      ? {
          key: 'billingPeriod',
          labelKey: 'accounts.detail_subscription_billing_period',
          value: billingLabel,
          valueKind: billingLabel.startsWith('accounts.') ? 'i18n' : 'text',
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

export const buildCodexSubscriptionTabFields = (
  entry: CodexSubscriptionEntry
): AccountDetailField[] =>
  entry.status === 'ready' ? buildCodexSubscriptionDetailFields(entry.record) : [];
