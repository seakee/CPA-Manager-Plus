import type { TFunction } from 'i18next';
import type { MonitoringEventRow } from '@/features/monitoring/hooks/useMonitoringData';
import type { AccountDisplayMode } from '@/features/monitoring/accountOverviewState';
import {
  isGenericMonitoringProviderLabel,
  isKeyDisambiguatedLabel,
  isRedundantMonitoringLabel,
} from '@/features/monitoring/model/sourceDisplay';
import { shouldPreferApiKeyAlias } from '@/features/monitoring/model/apiKeys';

const hasReadableRealtimeValue = (value: string | null | undefined) => {
  const trimmed = String(value || '').trim();
  return Boolean(trimmed) && trimmed !== '-';
};

const firstReadable = (...values: Array<string | null | undefined>) =>
  values.find(hasReadableRealtimeValue)?.trim() || '';

const isOpaqueUsageSource = (value: string | null | undefined) =>
  /^(?:h|k|m):/i.test(String(value || '').trim());

const firstReadableIdentity = (...values: Array<string | null | undefined>) =>
  values.find((value) => hasReadableRealtimeValue(value) && !isOpaqueUsageSource(value))?.trim() ||
  '';

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
  > &
    Partial<
      Pick<
        MonitoringEventRow,
        'apiKeyLabel' | 'apiKeyMasked' | 'clientIp' | 'userAgent' | 'xForwardedFor'
      >
    >,
  t: TFunction,
  accountDisplayMode: AccountDisplayMode = 'masked'
) => {
  const channel = hasReadableRealtimeValue(row.channel) ? row.channel.trim() : '';
  const provider = hasReadableRealtimeValue(row.provider) ? row.provider.trim() : '';
  const host = hasReadableRealtimeValue(row.channelHost) ? row.channelHost.trim() : '';
  const fullAccount = firstReadableIdentity(row.account, row.authLabel, row.accountMasked);
  const maskedAccount = firstReadableIdentity(row.accountMasked, row.authLabel, row.account);
  const account = accountDisplayMode === 'full' ? fullAccount : maskedAccount;
  const fullSource = firstReadableIdentity(
    row.source,
    row.account,
    row.authLabel,
    row.sourceMasked
  );
  const maskedSource = firstReadableIdentity(
    row.sourceMasked,
    row.accountMasked,
    row.authLabel,
    row.source
  );
  const source = accountDisplayMode === 'full' ? fullSource : maskedSource;
  const apiKeyAlias = shouldPreferApiKeyAlias(row.apiKeyLabel || '', row.apiKeyMasked || '')
    ? firstReadableIdentity(row.apiKeyLabel)
    : '';
  const opaqueSource = [
    row.source,
    row.sourceMasked,
    row.account,
    row.accountMasked,
    row.authLabel,
  ].find((value) => hasReadableRealtimeValue(value) && isOpaqueUsageSource(value));
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
      apiKeyAlias,
      channel,
      provider,
      opaqueSource
    ) || '-';
  const metaCandidate = provider
    ? { value: provider, label: t('monitoring.filter_provider') }
    : [
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
  const clientIp = row.clientIp?.trim() || '';
  const xForwardedFor = row.xForwardedFor?.trim() || '';
  const userAgent = row.userAgent?.trim() || '';
  const requestMetadata =
    accountDisplayMode === 'full'
      ? [
          hasReadableRealtimeValue(clientIp)
            ? `${t('monitoring.client_ip')}: ${clientIp}`
            : '',
          hasReadableRealtimeValue(xForwardedFor)
            ? `${t('monitoring.x_forwarded_for_unverified')}: ${xForwardedFor}`
            : '',
          hasReadableRealtimeValue(userAgent)
            ? `${t('monitoring.user_agent')}: ${userAgent}`
            : '',
        ]
      : [];
  const requestMetadataTitle = requestMetadata.filter(hasReadableRealtimeValue).join('\n');
  const title = Array.from(
    new Set(
      [
        primary,
        meta,
        fullSource,
        maskedSource,
        fullAccount,
        maskedAccount,
        opaqueSource,
        host,
        provider,
        ...requestMetadata,
      ].filter(hasReadableRealtimeValue)
    )
  ).join(' · ');

  return {
    primary,
    meta,
    title,
    requestMetadataTitle,
  };
};
