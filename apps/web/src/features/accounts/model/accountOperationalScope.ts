import {
  getAuthFileCodexInspectionKey,
  getAuthFileCodexInspectionKeyForFile,
  getAuthFileCodexInspectionKeyForIdentity,
} from '@/features/authFiles/model/credentialStatus';
import type { AccountRow } from './accountRows';

export interface AccountOperationalItem {
  authFileName: string;
  runtimeId?: unknown;
  provider?: unknown;
  authIndex?: unknown;
  accountId?: unknown;
  accountIdSnapshot?: unknown;
  accountSnapshot?: unknown;
}

export const getAccountOperationalItemIdentityKey = (item: AccountOperationalItem): string =>
  getAuthFileCodexInspectionKeyForIdentity({
    fileName: item.authFileName,
    runtimeId: typeof item.runtimeId === 'string' ? item.runtimeId : null,
    provider: typeof item.provider === 'string' ? item.provider : null,
    authIndex:
      typeof item.authIndex === 'string' || typeof item.authIndex === 'number'
        ? item.authIndex
        : null,
    accountId:
      typeof item.accountId === 'string'
        ? item.accountId
        : typeof item.accountIdSnapshot === 'string'
          ? item.accountIdSnapshot
          : null,
    accountSnapshot: typeof item.accountSnapshot === 'string' ? item.accountSnapshot : null,
  });

export const accountOperationalItemMatchesRow = (
  row: AccountRow,
  item: AccountOperationalItem
): boolean =>
  getAuthFileCodexInspectionKeyForFile(row.raw) === getAccountOperationalItemIdentityKey(item);

export const buildAccountOperationalScopeKeys = (rows: AccountRow[]): Map<string, string[]> => {
  const eligibleRows = rows.filter((row) => !row.runtimeOnly);
  const exactCounts = new Map<string, number>();
  const fallbackCounts = new Map<string, number>();
  eligibleRows.forEach((row) => {
    const exactKey = getAuthFileCodexInspectionKeyForFile(row.raw);
    exactCounts.set(exactKey, (exactCounts.get(exactKey) ?? 0) + 1);
    const fallbackKey = getAuthFileCodexInspectionKey(row.fileName, null);
    fallbackCounts.set(fallbackKey, (fallbackCounts.get(fallbackKey) ?? 0) + 1);
  });

  return new Map(
    eligibleRows.map((row) => {
      const exactKey = getAuthFileCodexInspectionKeyForFile(row.raw);
      const fallbackKey = getAuthFileCodexInspectionKey(row.fileName, null);
      // A duplicate exact key means the current response does not contain
      // enough credential evidence to tell these rows apart. In particular,
      // Codex workspace-only rows intentionally share a fallback key because
      // account_id is a Workspace, not a member identity. Do not fan out an
      // operational item to every row (or pick one arbitrarily).
      const keys = exactCounts.get(exactKey) === 1 ? [exactKey] : [];
      if (fallbackKey !== exactKey && fallbackCounts.get(fallbackKey) === 1) {
        keys.push(fallbackKey);
      }
      return [row.selectionKey, keys];
    })
  );
};

export const buildAccountOperationalItemsByRowKey = <T extends AccountOperationalItem>(
  rows: AccountRow[],
  items: T[]
): Map<string, T[]> => {
  const itemsByScopeKey = new Map<string, T[]>();
  items.forEach((item) => {
    if (!item.authFileName) return;
    const key = getAccountOperationalItemIdentityKey(item);
    itemsByScopeKey.set(key, [...(itemsByScopeKey.get(key) ?? []), item]);
  });

  const scopeKeysByRowKey = buildAccountOperationalScopeKeys(rows);
  return new Map(
    Array.from(scopeKeysByRowKey, ([rowKey, scopeKeys]) => [
      rowKey,
      scopeKeys.flatMap((key) => itemsByScopeKey.get(key) ?? []),
    ])
  );
};
