import type { TFunction } from 'i18next';
import type { MonitoringEventRow } from '@/features/monitoring/hooks/useMonitoringData';
import type { AccountDisplayMode } from '@/features/monitoring/accountOverviewState';
import {
  isGenericMonitoringProviderLabel,
  isKeyDisambiguatedLabel,
  isOpenAICompatibleRuntimeLabel,
  isProviderLikeMonitoringLabel,
  isRedundantMonitoringLabel,
} from '@/features/monitoring/model/sourceDisplay';
import { isOpaqueUsageSourceId } from '@/utils/usage';

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
    | 'providerIdentity'
    | 'source'
    | 'sourceMasked'
  > &
    Partial<
      Pick<
        MonitoringEventRow,
        | 'clientIp'
        | 'userAgent'
        | 'xForwardedFor'
        | 'sessionId'
        | 'parentSessionId'
        | 'generate'
        | 'stream'
      >
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
    channel && !isProviderLikeMonitoringLabel(channel, provider) ? channel : '';
  const nonGenericSource =
    source && !isProviderLikeMonitoringLabel(source, provider) ? source : '';
  const readableNonGenericSource =
    nonGenericSource && !isOpaqueUsageSourceId(nonGenericSource) ? nonGenericSource : '';
  const readableAccount = account && !isOpaqueUsageSourceId(account) ? account : '';
  const keyDisambiguatedSource =
    readableNonGenericSource &&
    (isKeyDisambiguatedLabel(readableNonGenericSource, channel) ||
      isKeyDisambiguatedLabel(readableNonGenericSource, host) ||
      isKeyDisambiguatedLabel(readableNonGenericSource, readableAccount))
      ? readableNonGenericSource
      : '';
  const opaqueSource = isOpaqueUsageSourceId(source)
    ? source
    : isOpaqueUsageSourceId(row.source)
    ? row.source
    : isOpaqueUsageSourceId(account)
    ? account
    : '';
  const primary =
    firstReadable(
      keyDisambiguatedSource,
      nonGenericChannel,
      host,
      readableNonGenericSource,
      readableAccount,
      provider && !isGenericMonitoringProviderLabel(provider) ? provider : '',
      channel,
      provider,
      opaqueSource
    ) || '-';
  const metaCandidate =
    provider &&
    (!isRedundantMonitoringLabel(provider, primary) ||
      !isOpenAICompatibleRuntimeLabel(row.providerIdentity, primary))
      ? { value: provider, label: t('monitoring.filter_provider') }
      : [
          { value: host, label: t('monitoring.column_host') },
          { value: readableAccount, label: '' },
          { value: readableNonGenericSource, label: t('monitoring.source') },
          { value: opaqueSource, label: t('monitoring.source') },
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
  const sessionId = row.sessionId?.trim() || '';
  const parentSessionId = row.parentSessionId?.trim() || '';
  const generateText =
    typeof row.generate === 'boolean' ? t(row.generate ? 'common.yes' : 'common.no') : '';
  const streamText =
    typeof row.stream === 'boolean' ? t(row.stream ? 'common.yes' : 'common.no') : '';
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
          hasReadableRealtimeValue(sessionId)
            ? `${t('monitoring.session_id')}: ${sessionId}`
            : '',
          hasReadableRealtimeValue(parentSessionId)
            ? `${t('monitoring.parent_session_id')}: ${parentSessionId}`
            : '',
          hasReadableRealtimeValue(generateText)
            ? `${t('monitoring.generate')}: ${generateText}`
            : '',
          hasReadableRealtimeValue(streamText)
            ? `${t('monitoring.stream')}: ${streamText}`
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
