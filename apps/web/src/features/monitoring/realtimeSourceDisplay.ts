import type { TFunction } from 'i18next';
import type { MonitoringEventRow } from '@/features/monitoring/hooks/useMonitoringData';
import type { AccountDisplayMode } from '@/features/monitoring/accountOverviewState';
import {
  isGenericMonitoringProviderLabel,
  isKeyDisambiguatedLabel,
  isRedundantMonitoringLabel,
} from '@/features/monitoring/model/sourceDisplay';

const hasReadableRealtimeValue = (value: string | null | undefined) => {
  const trimmed = String(value || '').trim();
  return Boolean(trimmed) && trimmed !== '-';
};

const firstReadable = (...values: Array<string | null | undefined>) =>
  values.find(hasReadableRealtimeValue)?.trim() || '';

export const buildRealtimeSourceDisplay = (
  row: Pick<
    MonitoringEventRow,
    | 'account'
    | 'accountMasked'
    | 'authLabel'
    | 'channel'
    | 'channelHost'
    | 'provider'
    | 'source'
    | 'sourceMasked'
  >,
  t: TFunction,
  accountDisplayMode: AccountDisplayMode = 'masked'
) => {
  const channel = hasReadableRealtimeValue(row.channel) ? row.channel.trim() : '';
  const provider = hasReadableRealtimeValue(row.provider) ? row.provider.trim() : '';
  const host = hasReadableRealtimeValue(row.channelHost) ? row.channelHost.trim() : '';
  const fullAccount = firstReadable(row.account, row.authLabel, row.accountMasked);
  const maskedAccount = firstReadable(row.accountMasked, row.authLabel, row.account);
  const account = accountDisplayMode === 'full' ? fullAccount : maskedAccount;
  const fullSource = firstReadable(row.source, row.account, row.authLabel, row.sourceMasked);
  const maskedSource = firstReadable(row.sourceMasked, row.accountMasked, row.authLabel, row.source);
  const source = accountDisplayMode === 'full' ? fullSource : maskedSource;
  const nonGenericChannel =
    channel && !isGenericMonitoringProviderLabel(channel) ? channel : '';
  const nonGenericSource = source && !isGenericMonitoringProviderLabel(source) ? source : '';
  const keyDisambiguatedSource =
    nonGenericSource &&
    (isKeyDisambiguatedLabel(nonGenericSource, channel) ||
      isKeyDisambiguatedLabel(nonGenericSource, host) ||
      isKeyDisambiguatedLabel(nonGenericSource, account))
      ? nonGenericSource
      : '';
  const primary =
    firstReadable(
      keyDisambiguatedSource,
      nonGenericChannel,
      host,
      nonGenericSource,
      provider && !isGenericMonitoringProviderLabel(provider) ? provider : '',
      account || '',
      channel,
      provider
    ) || '-';
  const metaCandidate = [
    { value: provider, label: t('monitoring.filter_provider') },
    { value: host, label: t('monitoring.column_host') },
    { value: account, label: '' },
    { value: source, label: t('monitoring.source') },
  ].find(
    (candidate) =>
      candidate.value && !isRedundantMonitoringLabel(candidate.value, primary)
  );
  const meta =
    metaCandidate && metaCandidate.label
      ? `${metaCandidate.label}: ${metaCandidate.value}`
      : metaCandidate?.value || '';
  const title = Array.from(
    new Set(
      [
        primary,
        meta,
        fullSource,
        maskedSource,
        fullAccount,
        maskedAccount,
        host,
        provider,
      ].filter(hasReadableRealtimeValue)
    )
  ).join(' · ');

  return {
    primary,
    meta,
    title,
  };
};
