import type { TFunction } from 'i18next';
import type { XaiBillingSummary } from '@/types';
import { probeXaiInference, probeXaiQuota } from '@/utils/quota/providerRequests';
import { formatQuotaResetTime } from '@/utils/quota/formatters';
import { XaiProbeError } from '@/utils/quota/xaiErrors';
import { formatXaiProbeIssue } from '@/utils/quota/xaiPresentation';
import type {
  CodexInspectionAction,
  CodexInspectionAccount,
  CodexInspectionLogLevel,
  CodexInspectionQuotaWindow,
  CodexInspectionResultItem,
  CodexInspectionSettings,
} from '@/features/monitoring/codexInspection';

type LogHandler = (level: CodexInspectionLogLevel, message: string) => void;

const MAX_INSPECTION_ERROR_DETAIL_LENGTH = 2048;
const identityT = ((key: string) => key) as TFunction;

const formatXaiInspectionAction = (action: CodexInspectionAction, t: TFunction) => {
  switch (action) {
    case 'delete':
      return t('monitoring.codex_inspection_action_delete');
    case 'disable':
      return t('monitoring.codex_inspection_action_disable');
    case 'enable':
      return t('monitoring.codex_inspection_action_enable');
    case 'reauth':
      return t('monitoring.codex_inspection_action_reauth');
    case 'keep':
    default:
      return t('monitoring.codex_inspection_action_keep');
  }
};

const truncateDetail = (value: unknown) => {
  const text = String(value ?? '').trim();
  if (text.length <= MAX_INSPECTION_ERROR_DETAIL_LENGTH) return text;
  return `${text.slice(0, MAX_INSPECTION_ERROR_DETAIL_LENGTH - 3)}...`;
};

const finitePercent = (value: number | null | undefined) =>
  typeof value === 'number' && Number.isFinite(value) ? value : null;

const resolveXaiUsedPercent = (summary: XaiBillingSummary): number | null => {
  const values = [
    summary.usagePercent,
    summary.usedPercent,
    summary.onDemandUsedPercent,
    ...summary.productUsage.map((item) => item.usagePercent),
  ].flatMap((value) => {
    const normalized = finitePercent(value);
    return normalized === null ? [] : [normalized];
  });
  return values.length > 0 ? Math.max(...values) : null;
};

const buildXaiQuotaWindows = (summary: XaiBillingSummary): CodexInspectionQuotaWindow[] => {
  const windows: CodexInspectionQuotaWindow[] = [];
  const weeklyPercent = finitePercent(summary.usagePercent);
  if (weeklyPercent !== null || summary.periodType === 'weekly') {
    windows.push({
      id: 'xai-weekly',
      labelKey: 'xai_quota.weekly_limit',
      usedPercent: weeklyPercent,
      resetLabel: formatQuotaResetTime(summary.periodEnd),
      limitWindowSeconds: null,
    });
  }

  const monthlyPercent = finitePercent(summary.usedPercent);
  if (monthlyPercent !== null || summary.monthlyLimitCents !== null) {
    windows.push({
      id: 'xai-monthly',
      labelKey: 'xai_quota.monthly_limit',
      usedPercent: monthlyPercent,
      resetLabel: formatQuotaResetTime(summary.billingPeriodEnd),
      limitWindowSeconds: null,
    });
  }

  const onDemandPercent = finitePercent(summary.onDemandUsedPercent);
  if (
    onDemandPercent !== null ||
    (summary.onDemandCapCents !== null && summary.onDemandCapCents > 0)
  ) {
    windows.push({
      id: 'xai-on-demand',
      labelKey: 'xai_quota.on_demand_cap',
      usedPercent: onDemandPercent,
      resetLabel: formatQuotaResetTime(summary.billingPeriodEnd),
      limitWindowSeconds: null,
    });
  }

  summary.productUsage.forEach((item, index) => {
    windows.push({
      id: `xai-product-${index}`,
      labelKey: 'xai_quota.product_usage',
      labelParams: { product: item.product },
      usedPercent: finitePercent(item.usagePercent),
      resetLabel: formatQuotaResetTime(summary.periodEnd),
      limitWindowSeconds: null,
    });
  });

  return windows;
};

const withRetry = async <T>(
  retries: number,
  task: () => Promise<T>,
  shouldRetry: (error: unknown) => boolean = () => true
): Promise<T> => {
  let lastError: unknown;
  for (let attempt = 0; attempt <= retries; attempt += 1) {
    try {
      return await task();
    } catch (error) {
      lastError = error;
      if (attempt === retries || !shouldRetry(error)) break;
    }
  }
  throw lastError;
};

const shouldRetryXaiInference = (error: unknown) =>
  error instanceof XaiProbeError &&
  [
    'upstream_error',
    'rate_limited',
    'probe_invalid',
    'model_unavailable',
    'protocol_changed',
  ].includes(error.decision.classification);

const shouldRetryXaiBilling = (error: unknown) =>
  !(error instanceof XaiProbeError) ||
  [
    'upstream_error',
    'rate_limited',
    'probe_invalid',
    'model_unavailable',
    'protocol_changed',
  ].includes(error.decision.classification);

const xaiActionReason = (classification: string, action: string, t: TFunction) => {
  switch (classification) {
    case 'free_quota_exhausted':
      return t(
        action === 'disable'
          ? 'monitoring.xai_inspection_reason_free_quota_disable'
          : 'monitoring.xai_inspection_reason_free_quota_disabled'
      );
    case 'spending_limit':
      return t(
        action === 'disable'
          ? 'monitoring.xai_inspection_reason_spending_limit_disable'
          : 'monitoring.xai_inspection_reason_spending_limit_disabled'
      );
    case 'auth_invalid':
      return t('monitoring.xai_inspection_reason_auth_invalid');
    case 'entitlement_denied':
      return t(
        action === 'disable'
          ? 'monitoring.xai_inspection_reason_entitlement_disable'
          : 'monitoring.xai_inspection_reason_entitlement_review'
      );
    case 'policy_denied':
      return t('monitoring.xai_inspection_reason_policy_denied');
    case 'permission_unknown':
      return t('monitoring.xai_inspection_reason_permission_unknown');
    case 'quota_or_entitlement_unknown':
      return t('monitoring.xai_inspection_reason_quota_unknown');
    case 'rate_limited':
      return t('monitoring.xai_inspection_reason_rate_limited');
    case 'client_outdated':
      return t('monitoring.xai_inspection_reason_client_outdated');
    case 'probe_invalid':
      return t('monitoring.xai_inspection_reason_probe_invalid');
    case 'upstream_error':
      return t('monitoring.xai_inspection_reason_upstream_error');
    case 'protocol_changed':
      return t('monitoring.xai_inspection_reason_protocol_changed');
    default:
      return t('monitoring.xai_inspection_reason_unknown');
  }
};

export const inspectSingleXaiAccount = async (
  account: CodexInspectionAccount,
  settings: CodexInspectionSettings,
  onLog?: LogHandler,
  t: TFunction = identityT
): Promise<CodexInspectionResultItem> => {
  if (!account.authIndex) {
    onLog?.(
      'warning',
      t('monitoring.xai_inspection_log_missing_auth_index', { account: account.displayAccount })
    );
    return {
      ...account,
      action: 'keep',
      actionReason: t('monitoring.xai_inspection_reason_missing_auth_index'),
      statusCode: null,
      usedPercent: null,
      isQuota: false,
      autoRecoverEligible: false,
      error: t('xai_quota.missing_auth_index'),
      planType: null,
      quotaWindows: [],
      errorKind: 'missing_auth_index',
      errorDetail: t('xai_quota.missing_auth_index'),
    };
  }

  const requestConfig = settings.timeout > 0 ? { timeout: settings.timeout } : undefined;
  let billingSummary: XaiBillingSummary | null = null;
  let billingStatusCode: number | null = null;
  let billingError: unknown = null;
  let billingPartial = false;
  let billingSource: 'billing' | 'official-api' = 'billing';
  try {
    const billing = await withRetry(
      settings.retries,
      () => probeXaiQuota(account.raw, t, requestConfig),
      shouldRetryXaiBilling
    );
    billingSummary = billing.summary;
    billingStatusCode = billing.statusCode ?? null;
    billingPartial = billing.partial;
    billingError = billing.blockingFailure ?? null;
    billingSource = billing.source;
  } catch (error) {
    billingError = error;
    // With inference enabled, billing remains supplementary quota evidence.
    // With inference disabled, the error is handled as the health result below.
  }

  try {
    if (!settings.xaiInferenceEnabled) {
      if (billingError) throw billingError;
      if (!billingSummary) throw new Error(t('xai_quota.empty_data'));

      const usedPercent = resolveXaiUsedPercent(billingSummary);
      const actionReason =
        billingSource === 'official-api'
          ? t(
              account.disabled
                ? 'monitoring.xai_inspection_reason_official_api_manual_disable'
                : 'monitoring.xai_inspection_reason_official_api_healthy'
            )
          : t(
              billingPartial
                ? 'monitoring.xai_inspection_reason_billing_partial'
                : 'monitoring.xai_inspection_reason_billing_healthy'
            );
      onLog?.(
        'info',
        t('monitoring.xai_inspection_log_result', {
          account: account.displayAccount,
          action: formatXaiInspectionAction('keep', t),
          percent: usedPercent === null ? '--' : `${usedPercent.toFixed(1)}%`,
        })
      );
      return {
        ...account,
        action: 'keep',
        actionReason,
        statusCode: billingStatusCode,
        usedPercent,
        isQuota: false,
        autoRecoverEligible: false,
        error: '',
        planType: null,
        quotaWindows: buildXaiQuotaWindows(billingSummary),
        errorKind:
          billingSource === 'official-api'
            ? 'official_api_healthy'
            : billingPartial
              ? 'billing_partial'
              : 'billing_healthy',
        errorDetail: '',
      };
    }

    const inference = await withRetry(
      settings.retries,
      () =>
        probeXaiInference(account.raw, t, requestConfig, {
          model: settings.xaiInferenceModel,
          prompt: settings.xaiInferencePrompt,
          userAgent: settings.xaiInferenceUserAgent,
          ...(billingSource === 'official-api' ? { routeMode: 'official' as const } : {}),
        }),
      shouldRetryXaiInference
    );
    const action = account.disabled && account.autoRecoverOwned ? 'enable' : 'keep';
    const actionReason =
      action === 'enable'
        ? t('monitoring.xai_inspection_reason_enable_owned')
        : account.disabled
          ? t('monitoring.xai_inspection_reason_inference_manual_disable')
          : t('monitoring.xai_inspection_reason_inference_healthy');
    const usedPercent = billingSummary ? resolveXaiUsedPercent(billingSummary) : null;
    onLog?.(
      action === 'enable' ? 'success' : 'info',
      t('monitoring.xai_inspection_log_result', {
        account: account.displayAccount,
        action: formatXaiInspectionAction(action, t),
        percent: usedPercent === null ? '--' : `${usedPercent.toFixed(1)}%`,
      })
    );
    return {
      ...account,
      action,
      actionReason,
      statusCode: inference.statusCode,
      usedPercent,
      isQuota: false,
      autoRecoverEligible: action === 'enable',
      error: '',
      planType: null,
      quotaWindows: billingSummary ? buildXaiQuotaWindows(billingSummary) : [],
      errorKind: 'inference_healthy',
      errorDetail: '',
    };
  } catch (error) {
    if (error instanceof XaiProbeError) {
      const { decision, envelope } = error;
      const action = decision.suggestedAction;
      const detail = truncateDetail(
        [envelope.code, envelope.type, envelope.message].filter(Boolean).join(' · ') ||
          error.message
      );
      const level: CodexInspectionLogLevel =
        action === 'disable' ? 'warning' : action === 'reauth' ? 'error' : 'warning';
      onLog?.(
        level,
        t('monitoring.xai_inspection_log_classified', {
          account: account.displayAccount,
          action: formatXaiInspectionAction(action, t),
          reason:
            formatXaiProbeIssue(decision.classification, t) ?? t('xai_quota.diagnostic_unknown'),
        })
      );
      return {
        ...account,
        action,
        actionReason: xaiActionReason(decision.classification, action, t),
        statusCode: envelope.statusCode,
        usedPercent: billingSummary ? resolveXaiUsedPercent(billingSummary) : null,
        isQuota: ['free_quota_exhausted', 'spending_limit'].includes(decision.classification),
        autoRecoverEligible: false,
        error: error.message,
        planType: null,
        quotaWindows: billingSummary ? buildXaiQuotaWindows(billingSummary) : [],
        errorKind: decision.classification,
        errorDetail: detail,
      };
    }

    const message =
      error instanceof Error ? error.message : String(error || t('xai_quota.load_failed'));
    onLog?.(
      'warning',
      t('monitoring.xai_inspection_log_request_error', {
        account: account.displayAccount,
        message,
      })
    );
    return {
      ...account,
      action: 'keep',
      actionReason: t('monitoring.xai_inspection_reason_request_error'),
      statusCode: null,
      usedPercent: billingSummary ? resolveXaiUsedPercent(billingSummary) : null,
      isQuota: false,
      autoRecoverEligible: false,
      error: message,
      planType: null,
      quotaWindows: billingSummary ? buildXaiQuotaWindows(billingSummary) : [],
      errorKind: 'request_error',
      errorDetail: truncateDetail(message),
    };
  }
};
