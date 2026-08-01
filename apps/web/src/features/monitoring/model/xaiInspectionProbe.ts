import type { TFunction } from 'i18next';
import type { XaiBillingSummary } from '@/types';
import { probeXaiInference, probeXaiQuota } from '@/utils/quota/providerRequests';
import { formatQuotaResetTime } from '@/utils/quota/formatters';
import { XaiProbeError } from '@/utils/quota/xaiErrors';
import { formatXaiProbeIssue } from '@/utils/quota/xaiPresentation';
import type {
  CodexInspectionAction,
  CodexInspectionAccount,
  CodexInspectionLogHandler,
  CodexInspectionLogLevel,
  CodexInspectionQuotaWindow,
  CodexInspectionResultItem,
  CodexInspectionSettings,
} from '@/features/monitoring/codexInspection';

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

const formatXaiInspectionSurface = (inferenceEnabled: boolean, t: TFunction) =>
  t(
    inferenceEnabled
      ? 'monitoring.xai_inspection_surface_inference'
      : 'monitoring.xai_inspection_surface_billing'
  );

type XaiInspectionLogDetailOptions = {
  account: CodexInspectionAccount;
  inspectionMode: 'billing' | 'identity' | 'inference' | 'skipped';
  healthEvidence: string;
  billingAvailable: boolean;
  billingPartial: boolean;
  inferenceEnabled: boolean;
  action: CodexInspectionAction;
  statusCode: number | null;
  usedPercent: number | null;
  inferenceHealthy?: boolean;
};

const buildXaiInspectionLogDetail = ({
  account,
  inspectionMode,
  healthEvidence,
  billingAvailable,
  billingPartial,
  inferenceEnabled,
  action,
  statusCode,
  usedPercent,
  inferenceHealthy,
}: XaiInspectionLogDetailOptions) => ({
  provider: 'xai',
  fileName: account.fileName,
  displayAccount: account.displayAccount,
  inspectionMode,
  healthEvidence,
  billingAvailable,
  billingPartial,
  inferenceEnabled,
  action,
  ...(statusCode !== null ? { statusCode } : {}),
  ...(usedPercent !== null ? { usedPercent } : {}),
  ...(typeof inferenceHealthy === 'boolean' ? { inferenceHealthy } : {}),
});

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

const xaiActionReason = (
  classification: string,
  action: string,
  inferenceEnabled: boolean,
  t: TFunction
) => {
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
      return t(
        inferenceEnabled
          ? 'monitoring.xai_inspection_reason_inference_quota_unknown'
          : 'monitoring.xai_inspection_reason_quota_unknown'
      );
    case 'rate_limited':
      return t('monitoring.xai_inspection_reason_rate_limited');
    case 'client_outdated':
      return t('monitoring.xai_inspection_reason_client_outdated');
    case 'probe_invalid':
      return t(
        inferenceEnabled
          ? 'monitoring.xai_inspection_reason_inference_probe_invalid'
          : 'monitoring.xai_inspection_reason_probe_invalid'
      );
    case 'model_unavailable':
      return t('monitoring.xai_inspection_reason_model_unavailable');
    case 'upstream_error':
      return t('monitoring.xai_inspection_reason_upstream_error');
    case 'protocol_changed':
      return t(
        inferenceEnabled
          ? 'monitoring.xai_inspection_reason_inference_protocol_changed'
          : 'monitoring.xai_inspection_reason_protocol_changed'
      );
    default:
      return t('monitoring.xai_inspection_reason_unknown');
  }
};

export const inspectSingleXaiAccount = async (
  account: CodexInspectionAccount,
  settings: CodexInspectionSettings,
  onLog?: CodexInspectionLogHandler,
  t: TFunction = identityT
): Promise<CodexInspectionResultItem> => {
  if (!account.authIndex) {
    onLog?.(
      'warning',
      t('monitoring.xai_inspection_log_missing_auth_index', { account: account.displayAccount }),
      buildXaiInspectionLogDetail({
        account,
        inspectionMode: 'skipped',
        healthEvidence: 'missing_auth_index',
        billingAvailable: false,
        billingPartial: false,
        inferenceEnabled: settings.xaiInferenceEnabled,
        action: 'keep',
        statusCode: null,
        usedPercent: null,
      })
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
    billingPartial = true;
    // With inference enabled, billing remains supplementary quota evidence.
    // With inference disabled, the error is handled as the health result below.
  }

  try {
    if (!settings.xaiInferenceEnabled) {
      if (billingError) throw billingError;
      if (!billingSummary) throw new Error(t('xai_quota.empty_data'));

      const usedPercent = resolveXaiUsedPercent(billingSummary);
      const healthEvidence =
        billingSource === 'official-api'
          ? 'official_api_healthy'
          : billingPartial
            ? 'billing_partial'
            : 'billing_healthy';
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
      const evidence = t(
        billingSource === 'official-api'
          ? 'monitoring.xai_inspection_evidence_official_api_healthy'
          : billingPartial
            ? 'monitoring.xai_inspection_evidence_billing_partial'
            : 'monitoring.xai_inspection_evidence_billing_healthy'
      );
      onLog?.(
        billingPartial ? 'warning' : 'info',
        t('monitoring.xai_inspection_log_result', {
          account: account.displayAccount,
          action: formatXaiInspectionAction('keep', t),
          evidence,
          percent: usedPercent === null ? '--' : `${usedPercent.toFixed(1)}%`,
        }),
        buildXaiInspectionLogDetail({
          account,
          inspectionMode: billingSource === 'official-api' ? 'identity' : 'billing',
          healthEvidence,
          billingAvailable: billingSource === 'billing',
          billingPartial,
          inferenceEnabled: false,
          action: 'keep',
          statusCode: billingStatusCode,
          usedPercent,
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
        errorKind: healthEvidence,
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
        evidence: t('monitoring.xai_inspection_evidence_inference_healthy'),
        percent: usedPercent === null ? '--' : `${usedPercent.toFixed(1)}%`,
      }),
      buildXaiInspectionLogDetail({
        account,
        inspectionMode: 'inference',
        healthEvidence: 'inference_healthy',
        billingAvailable: billingSummary !== null,
        billingPartial,
        inferenceEnabled: true,
        action,
        statusCode: inference.statusCode,
        usedPercent,
        inferenceHealthy: true,
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
      const action =
        account.disabled && decision.suggestedAction === 'disable'
          ? 'keep'
          : decision.suggestedAction;
      const issueSurface = settings.xaiInferenceEnabled ? 'inference' : 'billing';
      const detail = truncateDetail(
        [envelope.code, envelope.type, envelope.message].filter(Boolean).join(' · ') ||
          error.message
      );
      const level: CodexInspectionLogLevel =
        action === 'delete' || action === 'reauth' ? 'error' : 'warning';
      const usedPercent = billingSummary ? resolveXaiUsedPercent(billingSummary) : null;
      onLog?.(
        level,
        t('monitoring.xai_inspection_log_classified', {
          account: account.displayAccount,
          action: formatXaiInspectionAction(action, t),
          surface: formatXaiInspectionSurface(settings.xaiInferenceEnabled, t),
          reason:
            formatXaiProbeIssue(decision.classification, t, issueSurface) ??
            t('xai_quota.diagnostic_unknown'),
        }),
        buildXaiInspectionLogDetail({
          account,
          inspectionMode: settings.xaiInferenceEnabled ? 'inference' : 'billing',
          healthEvidence: decision.classification,
          billingAvailable: billingSummary !== null,
          billingPartial,
          inferenceEnabled: settings.xaiInferenceEnabled,
          action,
          statusCode: envelope.statusCode ?? null,
          usedPercent,
          ...(settings.xaiInferenceEnabled ? { inferenceHealthy: false } : {}),
        })
      );
      return {
        ...account,
        action,
        actionReason: xaiActionReason(
          decision.classification,
          action,
          settings.xaiInferenceEnabled,
          t
        ),
        statusCode: envelope.statusCode,
        usedPercent,
        isQuota: ['free_quota_exhausted', 'spending_limit', 'entitlement_denied'].includes(
          decision.classification
        ),
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
    const inferenceRequestFailed = settings.xaiInferenceEnabled;
    const usedPercent = billingSummary ? resolveXaiUsedPercent(billingSummary) : null;
    onLog?.(
      'warning',
      t('monitoring.xai_inspection_log_request_error', {
        account: account.displayAccount,
        surface: formatXaiInspectionSurface(inferenceRequestFailed, t),
        message,
      }),
      buildXaiInspectionLogDetail({
        account,
        inspectionMode: inferenceRequestFailed ? 'inference' : 'billing',
        healthEvidence: 'request_error',
        billingAvailable: billingSummary !== null,
        billingPartial,
        inferenceEnabled: inferenceRequestFailed,
        action: 'keep',
        statusCode: null,
        usedPercent,
        ...(inferenceRequestFailed ? { inferenceHealthy: false } : {}),
      })
    );
    return {
      ...account,
      action: 'keep',
      actionReason: t(
        inferenceRequestFailed
          ? 'monitoring.xai_inspection_reason_inference_request_error'
          : 'monitoring.xai_inspection_reason_request_error'
      ),
      statusCode: null,
      usedPercent,
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
