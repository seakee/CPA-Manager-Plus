import type { CodexQuotaState } from '@/types';
import type { AccountRow } from '@/features/accounts/model/accountRows';
import { buildAccountSubscriptionPresentation } from '@/features/accounts/model/accountSubscriptionPresentation';
import { normalizeAuthIndex } from '@/utils/quota/parsers';
import { resolveCodexChatgptAccountId } from '@/utils/quota/resolvers';

export const shouldShowCodexSubscriptionTab = (
  row: Pick<AccountRow, 'provider' | 'planType' | 'raw'>,
  codexQuota?: CodexQuotaState | null
): boolean => buildAccountSubscriptionPresentation({ row, codexQuota }).isPaidCodex;

export const collectCodexSubscriptionTargets = (
  rows: Array<Pick<AccountRow, 'provider' | 'planType' | 'raw' | 'authIndex'>>,
  resolveQuota?: (
    row: Pick<AccountRow, 'provider' | 'planType' | 'raw'>
  ) => CodexQuotaState | null | undefined
): Array<{ accountId: string; authIndex: string }> => {
  const targets = new Map<string, string>();
  for (const row of rows) {
    if (!shouldShowCodexSubscriptionTab(row, resolveQuota?.(row))) continue;
    const accountId = resolveCodexChatgptAccountId(row.raw);
    const authIndex = normalizeAuthIndex(row.authIndex ?? row.raw.auth_index ?? row.raw.authIndex);
    if (!accountId || !authIndex || targets.has(accountId)) continue;
    targets.set(accountId, authIndex);
  }
  return [...targets.entries()].map(([accountId, authIndex]) => ({ accountId, authIndex }));
};
