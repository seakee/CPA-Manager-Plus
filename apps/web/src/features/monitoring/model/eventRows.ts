import type { CredentialInfo } from '@/types/sourceInfo';
import { buildSourceInfoMap, resolveSourceDisplay } from '@/utils/sourceResolver';
import {
  calculateCost,
  normalizeAuthIndex,
  type ModelPrice,
  type UsageDetailWithEndpoint,
} from '@/utils/usage';
import { formatApiKeyHashLabel } from './base';
import { buildSearchText, maskAuthIndex, maskEmailLike, readString } from './base';
import { sanitizeApiKeyDisplayText, type ApiKeyDisplayInfo } from './apiKeys';
import { isKeyDisambiguatedLabel } from './sourceDisplay';
import { buildHourLabel, buildLocalDayKey } from './range';
import type { MonitoringAuthMeta, MonitoringChannelMeta, MonitoringEventRow } from './types';

const toDurationMs = (value: unknown): number | null => {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) return null;
  return value;
};

const calculateOutputTokensPerSecond = (
  outputTokens: number,
  latencyMs: number | null
): number | null => {
  if (outputTokens <= 0 || latencyMs === null || latencyMs <= 0) return null;

  return outputTokens / (latencyMs / 1000);
};

export const buildEventRows = (
  details: UsageDetailWithEndpoint[],
  authMetaMap: Map<string, MonitoringAuthMeta>,
  authFileMap: Map<string, CredentialInfo>,
  sourceInfoMap: ReturnType<typeof buildSourceInfoMap>,
  channelByAuthIndex: Map<string, MonitoringChannelMeta>,
  modelPrices: Record<string, ModelPrice>,
  apiKeyDisplayMap: Map<string, ApiKeyDisplayInfo>
) =>
  details
    .map((detail, index) => {
      const timestampMs =
        typeof detail.__timestampMs === 'number' && detail.__timestampMs > 0
          ? detail.__timestampMs
          : Date.parse(detail.timestamp);
      if (!Number.isFinite(timestampMs) || timestampMs <= 0) {
        return null;
      }

      const authIndex = normalizeAuthIndex(detail.auth_index) ?? '-';
      const authMeta = authMetaMap.get(authIndex);
      const sourceMeta = resolveSourceDisplay(
        detail.source,
        detail.auth_index,
        sourceInfoMap,
        authFileMap
      );
      const snapshotAccount = readString(detail.account_snapshot ?? detail.accountSnapshot);
      const snapshotLabel = readString(
        detail.auth_label_snapshot ??
          detail.authLabelSnapshot ??
          detail.auth_file_snapshot ??
          detail.authFileSnapshot
      );
      const snapshotProvider = readString(
        detail.auth_provider_snapshot ?? detail.authProviderSnapshot
      );
      const snapshotDisplay = snapshotAccount || snapshotLabel;
      const channelMeta =
        channelByAuthIndex.get(authIndex) ||
        (authMeta?.authIndex ? channelByAuthIndex.get(authMeta.authIndex) : undefined);
      const channelLabel =
        channelMeta?.name || authMeta?.provider || snapshotProvider || sourceMeta.type || '-';
      const resolvedSourceName = readString(sourceMeta.displayName);
      const labelCandidates = authMeta?.label || snapshotLabel || snapshotDisplay;
      // Prefer multi-key OpenAI-compatible disambiguation (e.g. "kuaileshifu #1") over the
      // bare provider/auth label so realtime cells match account overview identity.
      const sourceLabel =
        (resolvedSourceName &&
        (isKeyDisambiguatedLabel(resolvedSourceName, channelMeta?.name) ||
          isKeyDisambiguatedLabel(resolvedSourceName, channelMeta?.host) ||
          isKeyDisambiguatedLabel(resolvedSourceName, labelCandidates) ||
          isKeyDisambiguatedLabel(resolvedSourceName, authMeta?.account) ||
          isKeyDisambiguatedLabel(resolvedSourceName, snapshotAccount))
          ? resolvedSourceName
          : '') ||
        authMeta?.label ||
        snapshotDisplay ||
        resolvedSourceName ||
        authIndex;
      const sourceMasked = maskEmailLike(sourceLabel);
      const account = authMeta?.account || snapshotAccount || sourceLabel;
      const accountMasked = maskEmailLike(account);
      const apiKeyHash = readString(detail.api_key_hash ?? detail.apiKeyHash).toLowerCase();
      const apiKeyDisplay = apiKeyDisplayMap.get(apiKeyHash);
      const apiKeyLabel = sanitizeApiKeyDisplayText(
        apiKeyDisplay?.label || formatApiKeyHashLabel(apiKeyHash),
        formatApiKeyHashLabel(apiKeyHash)
      );
      const apiKeyMasked = sanitizeApiKeyDisplayText(
        apiKeyDisplay?.masked || apiKeyLabel,
        apiKeyLabel
      );
      const endpoint = readString(detail.__endpoint) || '-';
      const endpointMethod = readString(detail.__endpointMethod) || '-';
      const endpointPath = readString(detail.__endpointPath) || endpoint;
      const resolvedModel = readString(detail.__resolvedModel);
      const projectId = readString(detail.auth_project_id_snapshot ?? detail.authProjectIdSnapshot);
      const inputTokens = Math.max(Number(detail.tokens?.input_tokens) || 0, 0);
      const outputTokens = Math.max(Number(detail.tokens?.output_tokens) || 0, 0);
      const reasoningTokens = Math.max(Number(detail.tokens?.reasoning_tokens) || 0, 0);
      const cacheReadTokens = Math.max(Number(detail.tokens?.cache_read_tokens) || 0, 0);
      const cacheCreationTokens = Math.max(Number(detail.tokens?.cache_creation_tokens) || 0, 0);
      const cachedTokens = Math.max(
        Math.max(Number(detail.tokens?.cached_tokens) || 0, 0),
        Math.max(Number(detail.tokens?.cache_tokens) || 0, 0)
      );
      const explicitTotalTokens = Math.max(Number(detail.tokens?.total_tokens) || 0, 0);
      const totalTokens =
        explicitTotalTokens > 0
          ? explicitTotalTokens
          : inputTokens + outputTokens + reasoningTokens;
      const latencyMs = toDurationMs(detail.latency_ms);
      const ttftMs = toDurationMs(detail.ttft_ms);
      const tokensPerSecond = calculateOutputTokensPerSecond(outputTokens, latencyMs);
      const totalCost = calculateCost(detail, modelPrices);
      const statsIncluded = detail.failed === true || inputTokens > 0 || outputTokens > 0;
      const dayKey = buildLocalDayKey(timestampMs);
      const hourLabel = buildHourLabel(timestampMs);
      const sourceKey = sourceMeta.identityKey || `source:${sourceLabel}`;
      const taskKey = `${detail.timestamp}|${sourceKey}|${authIndex}`;
      const reasoningEffort = readString(detail.reasoning_effort ?? detail.reasoningEffort);
      const requestServiceTier = readString(
        detail.request_service_tier ?? detail.requestServiceTier
      );
      const responseServiceTier = readString(
        detail.response_service_tier ?? detail.responseServiceTier
      );
      const serviceTier =
        requestServiceTier ||
        readString(detail.service_tier ?? detail.serviceTier) ||
        responseServiceTier;
      const executorType = readString(detail.executor_type ?? detail.executorType);
      const failStatusCodeRaw = detail.fail_status_code ?? detail.failStatusCode;
      const failStatusCode =
        failStatusCodeRaw === null || failStatusCodeRaw === undefined
          ? Number.NaN
          : Number(failStatusCodeRaw);
      const normalizedFailStatusCode =
        Number.isFinite(failStatusCode) && failStatusCode > 0 ? failStatusCode : null;
      const failSummary = readString(detail.fail_summary ?? detail.failSummary);
      const responseMetadata = detail.response_metadata ?? detail.responseMetadata;
      const headerQuotaRecoverAtMsRaw =
        detail.header_quota_recover_at_ms ??
        detail.headerQuotaRecoverAtMs ??
        responseMetadata?.quota?.recover_at_ms;
      const headerQuotaRecoverAtMs =
        typeof headerQuotaRecoverAtMsRaw === 'number' && Number.isFinite(headerQuotaRecoverAtMsRaw)
          ? headerQuotaRecoverAtMsRaw
          : null;
      const headerQuotaUsedPercentRaw =
        detail.header_quota_used_percent ??
        detail.headerQuotaUsedPercent ??
        responseMetadata?.quota?.used_percent;
      const headerQuotaUsedPercent =
        typeof headerQuotaUsedPercentRaw === 'number' && Number.isFinite(headerQuotaUsedPercentRaw)
          ? headerQuotaUsedPercentRaw
          : null;
      const headerQuotaPlanType =
        readString(detail.header_quota_plan_type ?? detail.headerQuotaPlanType) ||
        readString(responseMetadata?.quota?.plan_type ?? responseMetadata?.quota?.active_limit);
      const headerErrorKind =
        readString(detail.header_error_kind ?? detail.headerErrorKind) ||
        readString(responseMetadata?.errors?.kind);
      const headerErrorCode =
        readString(detail.header_error_code ?? detail.headerErrorCode) ||
        readString(
          responseMetadata?.errors?.code ??
            responseMetadata?.errors?.ide_root_error_code ??
            responseMetadata?.errors?.ide_error_code ??
            responseMetadata?.errors?.authorization_error
        );
      const headerTraceId =
        readString(detail.header_trace_id ?? detail.headerTraceId) ||
        readString(responseMetadata?.trace?.primary_trace_id);

      return {
        id: `${detail.timestamp}-${detail.__modelName || '-'}-${sourceKey}-${authIndex}-${index}`,
        timestamp: detail.timestamp,
        timestampMs,
        dayKey,
        hourLabel,
        model: readString(detail.__modelName) || '-',
        resolvedModel: resolvedModel || undefined,
        endpoint,
        endpointMethod,
        endpointPath,
        sourceKey,
        source: sourceLabel,
        sourceMasked,
        account,
        accountMasked,
        authIndex,
        authIndexMasked: maskAuthIndex(authIndex),
        authLabel: authMeta?.label || snapshotLabel || sourceMasked,
        projectId,
        apiKeyHash,
        apiKeyLabel,
        apiKeyMasked,
        provider: authMeta?.provider || snapshotProvider || sourceMeta.type || '-',
        planType: authMeta?.planType || '-',
        channel: channelLabel,
        channelHost: channelMeta?.host || '-',
        channelDisabled: channelMeta?.disabled || false,
        failed: detail.failed === true,
        statsIncluded,
        latencyMs,
        ttftMs,
        tokensPerSecond,
        inputTokens,
        outputTokens,
        reasoningTokens,
        cachedTokens,
        cacheReadTokens,
        cacheCreationTokens,
        totalTokens,
        totalCost,
        reasoningEffort,
        serviceTier,
        requestServiceTier,
        responseServiceTier,
        executorType,
        failStatusCode: normalizedFailStatusCode,
        failSummary,
        responseMetadata,
        headerQuotaRecoverAtMs,
        headerQuotaUsedPercent,
        headerQuotaPlanType,
        headerErrorKind,
        headerErrorCode,
        headerTraceId,
        taskKey,
        searchText: buildSearchText(
          detail.__modelName,
          sourceLabel,
          authMeta?.account,
          authMeta?.label,
          authIndex,
          apiKeyHash,
          apiKeyLabel,
          apiKeyMasked,
          channelLabel,
          channelMeta?.host,
          endpointPath,
          endpointMethod,
          authMeta?.provider || snapshotProvider,
          authMeta?.planType,
          resolvedModel,
          projectId,
          reasoningEffort,
          serviceTier,
          requestServiceTier,
          responseServiceTier,
          executorType,
          normalizedFailStatusCode,
          failSummary,
          headerErrorKind,
          headerErrorCode,
          headerTraceId,
          headerQuotaPlanType
        ),
      } satisfies MonitoringEventRow;
    })
    .filter(Boolean) as MonitoringEventRow[];
