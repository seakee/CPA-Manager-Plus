import type { AccountDetailField } from '@/features/accounts/model/accountDetailViewModel';
import type { CodexSubscriptionEntry, CodexSubscriptionRecord } from './types';

const SUBSCRIPTION_DAY_MS = 86_400_000;

const CODEX_SUBSCRIPTION_SUMMARY_KEYS = new Set([
  'planType',
  'activeUntilMs',
  'billingPeriod',
  'willRenew',
]);

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

export const getCodexSubscriptionRemainingDays = (
  untilMs: number | null | undefined,
  nowMs = Date.now()
): number | null => {
  if (
    typeof untilMs !== 'number' ||
    !Number.isFinite(untilMs) ||
    !Number.isFinite(nowMs) ||
    untilMs <= nowMs
  ) {
    return null;
  }
  return Math.max(1, Math.ceil((untilMs - nowMs) / SUBSCRIPTION_DAY_MS));
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

export const buildCodexSubscriptionSummaryFields = (
  record: CodexSubscriptionRecord
): AccountDetailField[] => {
  const billingLabel = record.billingPeriod ? billingPeriodLabelKey(record.billingPeriod) : null;
  return [
    {
      key: 'planType',
      labelKey: 'accounts.detail_subscription_plan_type',
      value: record.planType,
    },
    {
      key: 'activeUntilMs',
      labelKey: 'accounts.detail_subscription_active_until',
      value: record.activeUntilMs,
      valueKind: record.activeUntilMs !== null ? 'quota_reset' : undefined,
    },
    {
      key: 'billingPeriod',
      labelKey: 'accounts.detail_subscription_billing_period',
      value: billingLabel,
      valueKind: billingLabel?.startsWith('accounts.') ? 'i18n' : billingLabel ? 'text' : undefined,
    },
    booleanField('willRenew', 'accounts.detail_subscription_will_renew', record.willRenew) ?? {
      key: 'willRenew',
      labelKey: 'accounts.detail_subscription_will_renew',
      value: null,
    },
  ];
};

export const buildCodexSubscriptionSecondaryFields = (
  record: CodexSubscriptionRecord
): AccountDetailField[] =>
  buildCodexSubscriptionDetailFields(record).filter(
    (field) => !CODEX_SUBSCRIPTION_SUMMARY_KEYS.has(field.key)
  );

export const buildCodexSubscriptionTabFields = (
  entry: CodexSubscriptionEntry
): AccountDetailField[] =>
  entry.status === 'ready' ? buildCodexSubscriptionDetailFields(entry.record) : [];
