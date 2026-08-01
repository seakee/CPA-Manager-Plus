import type { TFunction } from 'i18next';
import type { XaiBillingDiagnostic } from '@/types';
import type { XaiProbeClassification } from '@/utils/quota/xaiErrors';

export type XaiProbeIssueClassification =
  | Exclude<XaiProbeClassification, 'billing_healthy' | 'inference_healthy'>
  | 'billing_partial'
  | 'missing_auth_index'
  | 'request_error';

export type XaiProbeIssueSurface = 'billing' | 'inference';

const XAI_PROBE_ISSUE_KEYS = {
  free_quota_exhausted: 'xai_quota.diagnostic_free_quota_exhausted',
  spending_limit: 'xai_quota.diagnostic_spending_limit',
  auth_invalid: 'xai_quota.diagnostic_auth_invalid',
  entitlement_denied: 'xai_quota.diagnostic_entitlement_denied',
  policy_denied: 'xai_quota.diagnostic_policy_denied',
  permission_unknown: 'xai_quota.diagnostic_permission_unknown',
  quota_or_entitlement_unknown: 'xai_quota.diagnostic_quota_or_entitlement_unknown',
  rate_limited: 'xai_quota.diagnostic_rate_limited',
  client_outdated: 'xai_quota.diagnostic_client_outdated',
  probe_invalid: 'xai_quota.diagnostic_probe_invalid',
  model_unavailable: 'xai_quota.diagnostic_model_unavailable',
  upstream_error: 'xai_quota.diagnostic_upstream_error',
  protocol_changed: 'xai_quota.diagnostic_protocol_changed',
  billing_partial: 'xai_quota.diagnostic_billing_partial',
  missing_auth_index: 'xai_quota.diagnostic_missing_auth_index',
  request_error: 'xai_quota.diagnostic_request_error',
  unknown: 'xai_quota.diagnostic_unknown',
} satisfies Record<XaiProbeIssueClassification, string>;

const XAI_INFERENCE_PROBE_ISSUE_KEYS = {
  quota_or_entitlement_unknown: 'xai_quota.diagnostic_inference_quota_or_entitlement_unknown',
  probe_invalid: 'xai_quota.diagnostic_inference_probe_invalid',
  protocol_changed: 'xai_quota.diagnostic_inference_protocol_changed',
  request_error: 'xai_quota.diagnostic_inference_request_error',
} satisfies Partial<Record<XaiProbeIssueClassification, string>>;

export const XAI_PROBE_ISSUE_CLASSIFICATIONS = Object.keys(
  XAI_PROBE_ISSUE_KEYS
) as XaiProbeIssueClassification[];

export const getXaiProbeIssueKey = (
  classification: string,
  surface: XaiProbeIssueSurface = 'billing'
): string | null => {
  if (
    surface === 'inference' &&
    Object.prototype.hasOwnProperty.call(XAI_INFERENCE_PROBE_ISSUE_KEYS, classification)
  ) {
    return XAI_INFERENCE_PROBE_ISSUE_KEYS[
      classification as keyof typeof XAI_INFERENCE_PROBE_ISSUE_KEYS
    ];
  }
  return Object.prototype.hasOwnProperty.call(XAI_PROBE_ISSUE_KEYS, classification)
    ? XAI_PROBE_ISSUE_KEYS[classification as XaiProbeIssueClassification]
    : null;
};

export const formatXaiProbeIssue = (
  classification: string,
  t: TFunction,
  surface: XaiProbeIssueSurface = 'billing'
): string | null => {
  const key = getXaiProbeIssueKey(classification, surface);
  return key ? t(key) : null;
};

export const formatXaiBillingDiagnostics = (
  diagnostics: readonly XaiBillingDiagnostic[] | undefined,
  t: TFunction
) => {
  const messages = diagnostics?.map(
    (item) => formatXaiProbeIssue(item.classification, t) ?? t('xai_quota.diagnostic_unknown')
  );
  const uniqueMessages = [...new Set(messages)];
  return uniqueMessages.length > 0 ? uniqueMessages.join(' · ') : t('xai_quota.partial_unknown');
};
