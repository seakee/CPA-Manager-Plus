import type { CredentialInfo } from '@/types/sourceInfo';
import { buildSourceInfoMap, resolveSourceDisplay } from '@/utils/sourceResolver';
import { normalizeAuthIndex } from '@/utils/usage';
import { maskEmailLike, readString } from './base';
import type { MonitoringAuthMeta, MonitoringChannelMeta } from './types';

const GENERIC_PROVIDER_LABELS = new Set([
  'codex',
  'openai',
  'openai-compatibility',
  'gemini',
  'claude',
  'vertex',
  'xai',
  'x-ai',
  'grok',
]);

const hasReadableValue = (value: string | null | undefined) => {
  const trimmed = readString(value);
  return Boolean(trimmed) && trimmed !== '-';
};

export const isGenericMonitoringProviderLabel = (value: string) =>
  GENERIC_PROVIDER_LABELS.has(value.trim().toLowerCase());

/**
 * True when `refined` is a key/provider ordinal disambiguation of `base`
 * (for example base=`kuaileshifu`, refined=`kuaileshifu #1`).
 */
export const isKeyDisambiguatedLabel = (
  refined: string | null | undefined,
  base: string | null | undefined
) => {
  const refinedValue = readString(refined);
  const baseValue = readString(base);
  if (!refinedValue || !baseValue || refinedValue === baseValue) return false;
  return refinedValue.toLowerCase().startsWith(`${baseValue.toLowerCase()} #`);
};

export const isRedundantMonitoringLabel = (
  candidate: string | null | undefined,
  primary: string | null | undefined
) => {
  const candidateValue = readString(candidate);
  const primaryValue = readString(primary);
  if (!candidateValue || !primaryValue) return false;
  if (candidateValue === primaryValue) return true;
  return (
    isKeyDisambiguatedLabel(primaryValue, candidateValue) ||
    isKeyDisambiguatedLabel(candidateValue, primaryValue)
  );
};

const firstReadable = (...values: Array<string | null | undefined>) =>
  values.find(hasReadableValue)?.trim() || '';

export type MonitoringSourceDisplayInput = {
  source?: string | null;
  sourceHash?: string | null;
  apiKeyHash?: string | null;
  authIndex?: unknown;
  accountSnapshot?: string | null;
  authLabelSnapshot?: string | null;
  authProviderSnapshot?: string | null;
  channel?: string | null;
  authLabel?: string | null;
  account?: string | null;
  apiKeyAlias?: string | null;
};

export type MonitoringSourceDisplayContext = {
  authMetaMap: Map<string, MonitoringAuthMeta>;
  authFileMap?: Map<string, CredentialInfo>;
  sourceInfoMap?: ReturnType<typeof buildSourceInfoMap>;
  channelByAuthIndex: Map<string, MonitoringChannelMeta>;
  apiKeyAliasMap?: Map<string, string>;
};

export type MonitoringSourceDisplay = {
  primary: string;
  meta: string;
  title: string;
  sourceLabel: string;
  sourceMasked: string;
  account: string;
  accountMasked: string;
  authIndex: string;
  channel: string;
  channelHost: string;
  provider: string;
  fallbackId: string;
};

const shortHash = (value: string | null | undefined) => {
  const trimmed = readString(value);
  if (!trimmed) return '-';
  return trimmed.length <= 12 ? trimmed : `${trimmed.slice(0, 6)}...${trimmed.slice(-4)}`;
};

export const buildAuthFileMapFromMeta = (
  authMetaMap: Map<string, MonitoringAuthMeta>
): Map<string, CredentialInfo> => {
  const map = new Map<string, CredentialInfo>();
  authMetaMap.forEach((meta, authIndex) => {
    map.set(authIndex, {
      name: meta.label || meta.account || authIndex,
      type: meta.provider || '',
    });
  });
  return map;
};

export const buildMonitoringSourceDisplay = (
  input: MonitoringSourceDisplayInput,
  context: MonitoringSourceDisplayContext
): MonitoringSourceDisplay => {
  const authIndex = normalizeAuthIndex(input.authIndex) ?? '-';
  const authMeta = authIndex === '-' ? undefined : context.authMetaMap.get(authIndex);
  const channelMeta =
    authIndex === '-'
      ? undefined
      : context.channelByAuthIndex.get(authIndex) ||
        (authMeta?.authIndex ? context.channelByAuthIndex.get(authMeta.authIndex) : undefined);
  const sourceInfoMap = context.sourceInfoMap ?? buildSourceInfoMap({});
  const authFileMap = context.authFileMap ?? buildAuthFileMapFromMeta(context.authMetaMap);
  const sourceMeta = resolveSourceDisplay(
    readString(input.source),
    authIndex,
    sourceInfoMap,
    authFileMap
  );
  const apiKeyHash = readString(input.apiKeyHash).toLowerCase();
  const apiKeyAlias = firstReadable(
    input.apiKeyAlias,
    apiKeyHash ? context.apiKeyAliasMap?.get(apiKeyHash) : ''
  );
  const snapshotAccount = readString(input.accountSnapshot);
  const snapshotLabel = readString(input.authLabelSnapshot);
  const snapshotProvider = readString(input.authProviderSnapshot);
  const explicitChannel = readString(input.channel);
  const explicitLabel = readString(input.authLabel);
  const explicitAccount = readString(input.account);

  const account = firstReadable(
    authMeta?.account,
    explicitAccount,
    snapshotAccount,
    explicitLabel,
    snapshotLabel
  );
  const provider = firstReadable(authMeta?.provider, snapshotProvider, sourceMeta.type);
  const channel = firstReadable(channelMeta?.name, explicitChannel, provider);
  const channelHost = firstReadable(channelMeta?.host);
  const resolvedSourceName = firstReadable(sourceMeta.displayName);
  const labelCandidates = firstReadable(authMeta?.label, explicitLabel, snapshotLabel);
  // Prefer key-disambiguated source names (e.g. "kuaileshifu #1") over the bare
  // OpenAI-compatible provider/channel name when multi-key providers share a label.
  const sourceLabel = firstReadable(
    resolvedSourceName &&
      (isKeyDisambiguatedLabel(resolvedSourceName, channel) ||
        isKeyDisambiguatedLabel(resolvedSourceName, channelHost) ||
        isKeyDisambiguatedLabel(resolvedSourceName, labelCandidates) ||
        isKeyDisambiguatedLabel(resolvedSourceName, account))
      ? resolvedSourceName
      : '',
    labelCandidates,
    account,
    resolvedSourceName
  );
  const sourceMasked = maskEmailLike(sourceLabel || sourceMeta.displayName);
  const accountMasked = maskEmailLike(account || sourceLabel);
  const fallbackId = shortHash(input.sourceHash || input.apiKeyHash || authIndex);
  const nonGenericChannel =
    channel && !isGenericMonitoringProviderLabel(channel) ? channel : '';
  const nonGenericSource =
    sourceMasked && !isGenericMonitoringProviderLabel(sourceMasked) ? sourceMasked : '';
  const keyDisambiguatedSource =
    nonGenericSource &&
    (isKeyDisambiguatedLabel(nonGenericSource, channel) ||
      isKeyDisambiguatedLabel(nonGenericSource, channelHost) ||
      isKeyDisambiguatedLabel(nonGenericSource, labelCandidates) ||
      isKeyDisambiguatedLabel(nonGenericSource, account))
      ? nonGenericSource
      : '';
  const primary =
    firstReadable(
      keyDisambiguatedSource,
      nonGenericChannel,
      channelHost,
      nonGenericSource,
      provider && !isGenericMonitoringProviderLabel(provider) ? provider : '',
      accountMasked,
      apiKeyAlias,
      channel,
      provider,
      fallbackId
    ) || '-';
  const meta = firstReadable(
    provider && !isRedundantMonitoringLabel(provider, primary) ? provider : '',
    channelHost && !isRedundantMonitoringLabel(channelHost, primary) ? channelHost : '',
    accountMasked && !isRedundantMonitoringLabel(accountMasked, primary) ? accountMasked : '',
    sourceMasked && !isRedundantMonitoringLabel(sourceMasked, primary) ? sourceMasked : '',
    apiKeyAlias && !isRedundantMonitoringLabel(apiKeyAlias, primary) ? apiKeyAlias : '',
    channel && !isRedundantMonitoringLabel(channel, primary) ? channel : ''
  );
  const title = Array.from(
    new Set(
      [
        primary,
        meta,
        sourceMasked,
        accountMasked,
        channelHost,
        provider,
        authIndex !== '-' ? `#${shortHash(authIndex)}` : '',
        readString(input.sourceHash),
        readString(input.apiKeyHash),
      ].filter(hasReadableValue)
    )
  ).join(' · ');

  return {
    primary,
    meta,
    title,
    sourceLabel: sourceLabel || primary,
    sourceMasked: sourceMasked || primary,
    account: account || sourceLabel || primary,
    accountMasked: accountMasked || sourceMasked || primary,
    authIndex,
    channel: channel || '-',
    channelHost: channelHost || '-',
    provider: provider || '-',
    fallbackId,
  };
};
