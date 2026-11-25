import type { CredentialInfo } from '@/types/sourceInfo';
import { buildSourceInfoMap, resolveSourceDisplay } from '@/utils/sourceResolver';
import { normalizeAuthIndex } from '@/utils/usage';
import { maskEmailLike, readString } from './base';
import {
  formatApiKeyDisplayName,
  isMaskedApiKeyPreview,
  resolveApiKeyDisplayName,
  resolveOriginDefaultName,
  type ApiKeyDisplayInfo,
} from './apiKeys';
import type { MonitoringAuthMeta, MonitoringChannelMeta } from './types';

const GENERIC_PROVIDER_LABELS: Record<string, true> = {
  codex: true,
  antigravity: true,
  openai: true,
  'openai-compatibility': true,
  gemini: true,
  'gemini-apikey': true,
  'gemini-api-key': true,
  interactions: true,
  'gemini-interactions': true,
  'interactions-apikey': true,
  'interactions-api-key': true,
  claude: true,
  'claude-apikey': true,
  'claude-api-key': true,
  'codex-apikey': true,
  'codex-api-key': true,
  vertex: true,
  'vertex-apikey': true,
  'vertex-api-key': true,
  'vertex-compat': true,
  xai: true,
  'xai-apikey': true,
  'xai-api-key': true,
  'x-ai': true,
  grok: true,
  meta: true,
  'meta-apikey': true,
  'meta-api-key': true,
};

const hasReadableValue = (value: string | null | undefined) => {
  const trimmed = readString(value);
  return Boolean(trimmed) && trimmed !== '-';
};

export const isGenericMonitoringProviderLabel = (value: string) =>
  Object.prototype.hasOwnProperty.call(GENERIC_PROVIDER_LABELS, value.trim().toLowerCase());

export const isProviderLikeMonitoringLabel = (
  value: string | null | undefined,
  provider: string | null | undefined
) => {
  const candidate = readString(value);
  if (!candidate) return false;

  if (isGenericMonitoringProviderLabel(candidate)) {
    return true;
  }

  const providerValue = readString(provider);
  return Boolean(providerValue) && candidate.toLowerCase() === providerValue.toLowerCase();
};

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
  apiKeyMasked?: string | null;
  apiKeyOrigin?: string | null;
};

export type MonitoringSourceDisplayContext = {
  authMetaMap: Map<string, MonitoringAuthMeta>;
  authFileMap?: Map<string, CredentialInfo>;
  sourceInfoMap?: ReturnType<typeof buildSourceInfoMap>;
  channelByAuthIndex: Map<string, MonitoringChannelMeta>;
  apiKeyAliasMap?: Map<string, string>;
  apiKeyDisplayMap?: ReadonlyMap<string, ApiKeyDisplayInfo>;
  language?: string;
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
  apiKeyLabel: string;
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
  const snapshotProvider = readString(input.authProviderSnapshot);
  const sourceMeta = resolveSourceDisplay(
    readString(input.source),
    authIndex,
    sourceInfoMap,
    authFileMap,
    snapshotProvider
  );
  const apiKeyHash = readString(input.apiKeyHash).toLowerCase();
  const apiKeyDisplay = apiKeyHash ? context.apiKeyDisplayMap?.get(apiKeyHash) : undefined;
  const explicitAlias = readString(input.apiKeyAlias);
  const explicitMasked = readString(input.apiKeyMasked);
  const explicitOrigin = readString(input.apiKeyOrigin);
  const resolvedDirectoryAlias = apiKeyDisplay
    ? resolveApiKeyDisplayName(apiKeyDisplay, context.language)
    : '';
  const effectiveMasked = readString(apiKeyDisplay?.maskedPreview) || explicitMasked;
  const synthesizedSnapshotAlias =
    explicitAlias || explicitOrigin || effectiveMasked
      ? formatApiKeyDisplayName(
          explicitAlias ||
            resolveOriginDefaultName(explicitOrigin || apiKeyDisplay?.origin, context.language),
          effectiveMasked,
          apiKeyDisplay?.lifecycle === 'superseded',
          context.language,
          apiKeyDisplay?.lifecycle === 'rotated'
        )
      : '';
  const apiKeyAlias = firstReadable(
    explicitAlias && apiKeyDisplay?.label && explicitAlias === apiKeyDisplay.label
      ? resolvedDirectoryAlias
      : explicitAlias && effectiveMasked && !explicitAlias.includes(effectiveMasked)
        ? `${explicitAlias} (${effectiveMasked})`
        : explicitAlias,
    resolvedDirectoryAlias,
    synthesizedSnapshotAlias,
    apiKeyHash ? context.apiKeyAliasMap?.get(apiKeyHash) : ''
  );
  const snapshotAccount = readString(input.accountSnapshot);
  const snapshotLabel = readString(input.authLabelSnapshot);
  const explicitChannel = readString(input.channel);
  const explicitLabel = readString(input.authLabel);
  const explicitAccount = readString(input.account);

  const resolvedSourceName = firstReadable(sourceMeta.displayName);
  const configuredSourceName = Boolean(
    resolvedSourceName &&
      sourceMeta.identityKey &&
      !sourceMeta.identityKey.startsWith('source:') &&
      !sourceMeta.identityKey.startsWith('auth:')
  );
  const matchedConfiguredByAuthIndex = Boolean(
    authIndex !== '-' && sourceInfoMap.byAuthIndex.get(authIndex)
  );
  const isConfiguredApiKeySource =
    configuredSourceName && !authMeta && matchedConfiguredByAuthIndex;
  const account = firstReadable(
    authMeta?.account,
    isConfiguredApiKeySource ? '' : explicitAccount,
    isConfiguredApiKeySource ? '' : snapshotAccount,
    isConfiguredApiKeySource ? '' : explicitLabel,
    isConfiguredApiKeySource ? '' : snapshotLabel,
    isConfiguredApiKeySource ? resolvedSourceName : ''
  );
  const provider = firstReadable(authMeta?.provider, snapshotProvider, sourceMeta.type);
  const channel = firstReadable(
    channelMeta?.name,
    isConfiguredApiKeySource ? '' : explicitChannel,
    provider
  );
  const channelHost = firstReadable(channelMeta?.host);
  const labelCandidates = firstReadable(authMeta?.label, explicitLabel, snapshotLabel);
  const hasNonGenericSnapshotIdentity =
    (Boolean(labelCandidates) && !isProviderLikeMonitoringLabel(labelCandidates, provider)) ||
    (Boolean(account) && !isProviderLikeMonitoringLabel(account, provider));
  // Prefer live configured API-key provider aliases (and key-disambiguated names
  // such as "kuaileshifu #1") over stale historical snapshot/channel labels only
  // when authIndex matched a live configured API-key entry.
  const sourceLabel = firstReadable(
    isConfiguredApiKeySource ? resolvedSourceName : '',
    resolvedSourceName &&
      (isKeyDisambiguatedLabel(resolvedSourceName, channel) ||
        isKeyDisambiguatedLabel(resolvedSourceName, channelHost) ||
        isKeyDisambiguatedLabel(resolvedSourceName, labelCandidates) ||
        isKeyDisambiguatedLabel(resolvedSourceName, account) ||
        (configuredSourceName && !hasNonGenericSnapshotIdentity))
      ? resolvedSourceName
      : '',
    labelCandidates,
    account,
    resolvedSourceName
  );
  const sourceMasked = maskEmailLike(sourceLabel || sourceMeta.displayName);
  const accountMasked = maskEmailLike(account || sourceLabel);
  const maskedFallbackCandidate = firstReadable(
    apiKeyDisplay?.maskedPreview,
    explicitMasked,
    apiKeyDisplay?.masked
  );
  const fallbackId = readString(input.sourceHash)
    ? shortHash(input.sourceHash)
    : maskedFallbackCandidate && isMaskedApiKeyPreview(maskedFallbackCandidate)
      ? maskedFallbackCandidate
      : shortHash(maskedFallbackCandidate || input.apiKeyHash || authIndex);
  const nonGenericChannel = channel && !isGenericMonitoringProviderLabel(channel) ? channel : '';
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
      isConfiguredApiKeySource ? nonGenericSource : '',
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
        apiKeyAlias ? '' : readString(input.apiKeyHash),
        apiKeyAlias,
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
    apiKeyLabel: apiKeyAlias,
  };
};
