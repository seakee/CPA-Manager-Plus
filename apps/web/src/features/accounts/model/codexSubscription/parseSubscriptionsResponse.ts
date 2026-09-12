import { normalizeNumberValue, normalizeStringValue } from '@/utils/quota/parsers';
import { parseSubscriptionTimestampMs } from './parseSubscriptionTimestamp';
import { type CodexSubscriptionExtras, type CodexSubscriptionRecord } from './types';

const asRecord = (value: unknown): Record<string, unknown> | null =>
  value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;

const normalizeBooleanValue = (value: unknown): boolean | null => {
  if (value === undefined || value === null) return null;
  if (typeof value === 'boolean') return value;
  if (typeof value === 'number') return value !== 0;
  if (typeof value === 'string') {
    const normalized = value.trim().toLowerCase();
    if (['true', '1', 'yes', 'y', 'on'].includes(normalized)) return true;
    if (['false', '0', 'no', 'n', 'off'].includes(normalized)) return false;
  }
  return null;
};

const unwrapSubscriptionPayload = (
  payload: unknown,
  accountId: string
): Record<string, unknown> | null => {
  if (Array.isArray(payload)) {
    const records = payload
      .map(asRecord)
      .filter((item): item is Record<string, unknown> => item !== null);
    const matched = records.find((item) => {
      const candidate = normalizeStringValue(
        item.account_id ?? item.accountId ?? item.chatgpt_account_id ?? item.chatgptAccountId
      );
      return candidate === accountId;
    });
    return matched ?? records[0] ?? null;
  }

  const record = asRecord(payload);
  if (!record) return null;

  const nested = record.subscription ?? record.subscriptions;
  if (nested !== undefined) {
    return unwrapSubscriptionPayload(nested, accountId) ?? record;
  }
  return record;
};

const parseDiscountLabel = (value: unknown): string | null => {
  const text = normalizeStringValue(value);
  if (text) return text;
  const record = asRecord(value);
  if (!record) return null;
  return (
    normalizeStringValue(record.label ?? record.name ?? record.description ?? record.code) ??
    normalizeStringValue(record.percent_off ?? record.percentOff)
  );
};

const parseExtras = (payload: Record<string, unknown>): CodexSubscriptionExtras => {
  const seats = asRecord(payload.seats);
  return {
    seatsInUse: normalizeNumberValue(
      payload.seats_in_use ?? payload.seatsInUse ?? seats?.in_use ?? seats?.inUse
    ),
    seatsEntitled: normalizeNumberValue(
      payload.seats_entitled ?? payload.seatsEntitled ?? seats?.entitled
    ),
    isDelinquent: normalizeBooleanValue(payload.is_delinquent ?? payload.isDelinquent),
    gracePeriodEndMs: parseSubscriptionTimestampMs(
      payload.grace_period_end_timestamp ??
        payload.gracePeriodEndTimestamp ??
        payload.grace_period_end ??
        payload.gracePeriodEnd
    ),
    discountLabel: parseDiscountLabel(payload.discount),
  };
};

export const parseSubscriptionsResponse = (
  payload: unknown,
  accountId: string,
  fetchedAtMs: number
): CodexSubscriptionRecord | null => {
  const trimmedAccountId = accountId.trim();
  if (!trimmedAccountId) return null;

  const record = unwrapSubscriptionPayload(payload, trimmedAccountId);
  if (!record) return null;

  const planType = normalizeStringValue(record.plan_type ?? record.planType);
  const activeStartMs = parseSubscriptionTimestampMs(record.active_start ?? record.activeStart);
  const activeUntilMs = parseSubscriptionTimestampMs(record.active_until ?? record.activeUntil);
  const billingPeriod = normalizeStringValue(record.billing_period ?? record.billingPeriod);
  const willRenew = normalizeBooleanValue(record.will_renew ?? record.willRenew);
  const extras = parseExtras(record);

  if (
    planType === null &&
    activeStartMs === null &&
    activeUntilMs === null &&
    billingPeriod === null &&
    willRenew === null
  ) {
    return null;
  }

  return {
    accountId:
      normalizeStringValue(
        record.account_id ??
          record.accountId ??
          record.chatgpt_account_id ??
          record.chatgptAccountId
      ) ?? trimmedAccountId,
    planType,
    activeStartMs,
    activeUntilMs,
    billingPeriod,
    willRenew,
    fetchedAtMs,
    source: 'subscriptions',
    extras,
  };
};

export const resolveSubscriptionUntilMs = (
  record: CodexSubscriptionRecord | null | undefined
): number | null => record?.activeUntilMs ?? null;
