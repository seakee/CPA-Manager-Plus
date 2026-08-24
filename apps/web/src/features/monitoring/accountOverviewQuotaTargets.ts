import type { AuthFileItem } from '@/types';
import {
  isAntigravityFile,
  isClaudeFile,
  isCodexFile,
  isDisabledAuthFile,
  isKimiFile,
  isXaiFile,
  normalizeAuthIndex,
  resolveCodexChatgptAccountId,
  resolveCodexPlanType,
} from '@/utils/quota';
import type { MonitoringAccountAuthState } from './accountOverviewState';
import type { MonitoringAccountRow } from './hooks/useMonitoringData';

export type MonitoringAccountQuotaProvider = 'antigravity' | 'claude' | 'codex' | 'kimi' | 'xai';

export type MonitoringAccountQuotaTarget = {
  key: string;
  provider: MonitoringAccountQuotaProvider;
  authIndex: string;
  authLabel: string;
  fileName: string;
  file: AuthFileItem;
  accountId: string | null;
  planType: string | null;
};

const readAuthFileQuotaLabel = (file: AuthFileItem, authIndex: string) => {
  const candidates = [file.label, file.name, file.email, file.account, authIndex];
  for (const candidate of candidates) {
    const text =
      typeof candidate === 'string'
        ? candidate.trim()
        : candidate === null || candidate === undefined
          ? ''
          : String(candidate).trim();
    if (text) return text;
  }
  return authIndex;
};

export const resolveMonitoringAccountQuotaProvider = (
  file: AuthFileItem
): MonitoringAccountQuotaProvider | null => {
  if (isCodexFile(file)) return 'codex';
  if (isClaudeFile(file)) return 'claude';
  if (isAntigravityFile(file)) return 'antigravity';
  if (isKimiFile(file)) return 'kimi';
  if (isXaiFile(file)) return 'xai';
  return null;
};

const isQuotaTargetable = (file: AuthFileItem) => {
  if (isDisabledAuthFile(file)) return false;
  return true;
};

const resolveActiveQuotaProvidersForRow = (
  row: MonitoringAccountRow,
  authState: MonitoringAccountAuthState | undefined
): Set<MonitoringAccountQuotaProvider> => {
  const activeProviders = new Set<MonitoringAccountQuotaProvider>();
  if (!authState) return activeProviders;

  const rowAuthIndices = new Set(
    row.authIndices
      .map((value) => normalizeAuthIndex(value))
      .filter((value): value is string => Boolean(value))
  );
  if (rowAuthIndices.size === 0) return activeProviders;

  authState.files.forEach((file) => {
    const authIndex = normalizeAuthIndex(file['auth_index'] ?? file.authIndex);
    if (!authIndex || !rowAuthIndices.has(authIndex)) return;

    const provider = resolveMonitoringAccountQuotaProvider(file);
    if (provider) activeProviders.add(provider);
  });

  return activeProviders;
};

export const buildMonitoringAccountQuotaTargetsByRowId = (
  rows: MonitoringAccountRow[],
  authStateByRowId: Map<string, MonitoringAccountAuthState>
) =>
  new Map(
    rows.map((row) => {
      const bucket = new Map<string, MonitoringAccountQuotaTarget>();
      const authState = authStateByRowId.get(row.id);
      const activeProviders = resolveActiveQuotaProvidersForRow(row, authState);

      authState?.files.forEach((file) => {
        const authIndex = normalizeAuthIndex(file['auth_index'] ?? file.authIndex);
        const provider = resolveMonitoringAccountQuotaProvider(file);
        if (!authIndex || !provider || !activeProviders.has(provider)) return;
        if (!isQuotaTargetable(file)) return;

        const dedupeKey = `${provider}::${authIndex}::${file.name}`;
        if (bucket.has(dedupeKey)) return;

        bucket.set(dedupeKey, {
          key: dedupeKey,
          provider,
          authIndex,
          authLabel: readAuthFileQuotaLabel(file, authIndex),
          fileName: file.name,
          file,
          accountId: provider === 'codex' ? resolveCodexChatgptAccountId(file) : null,
          planType: provider === 'codex' ? resolveCodexPlanType(file) : null,
        });
      });

      return [
        row.id,
        Array.from(bucket.values()).sort(
          (left, right) =>
            left.authLabel.localeCompare(right.authLabel) ||
            left.provider.localeCompare(right.provider)
        ),
      ] as const;
    })
  );
