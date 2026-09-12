export type CodexSubscriptionRecord = {
  accountId: string;
  planType: string | null;
  activeStartMs: number | null;
  activeUntilMs: number | null;
  billingPeriod: string | null;
  willRenew: boolean | null;
  fetchedAtMs: number;
  source: 'subscriptions';
  extras: CodexSubscriptionExtras;
};

export type CodexSubscriptionExtras = {
  seatsInUse: number | null;
  seatsEntitled: number | null;
  isDelinquent: boolean | null;
  gracePeriodEndMs: number | null;
  discountLabel: string | null;
};

export type CodexSubscriptionEntry =
  | { status: 'idle' }
  | { status: 'loading'; accountId: string; inflightSinceMs: number }
  | { status: 'ready'; record: CodexSubscriptionRecord }
  | { status: 'soft_failed'; accountId: string; failedAtMs: number; errorKind: string };

export type CodexSubscriptionFetchInput = {
  accountId: string;
  authIndex: string;
  nowMs?: number;
};

export const CODEX_SUBSCRIPTION_TTL_MS = 30 * 60 * 1000;

export const emptyCodexSubscriptionExtras = (): CodexSubscriptionExtras => ({
  seatsInUse: null,
  seatsEntitled: null,
  isDelinquent: null,
  gracePeriodEndMs: null,
  discountLabel: null,
});
