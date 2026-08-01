import type { TFunction } from 'i18next';
import { describe, expect, it } from 'vitest';
import en from '@/i18n/locales/en.json';
import ru from '@/i18n/locales/ru.json';
import zhCN from '@/i18n/locales/zh-CN.json';
import zhTW from '@/i18n/locales/zh-TW.json';
import {
  formatXaiBillingDiagnostics,
  formatXaiProbeIssue,
  getXaiProbeIssueKey,
  XAI_PROBE_ISSUE_CLASSIFICATIONS,
} from './xaiPresentation';

const locales = { en, ru, zhCN, zhTW };

const getPath = (value: unknown, path: string): unknown =>
  path.split('.').reduce<unknown>((current, segment) => {
    if (!current || typeof current !== 'object' || Array.isArray(current)) return undefined;
    return (current as Record<string, unknown>)[segment];
  }, value);

const placeholders = (value: string) =>
  [...value.matchAll(/{{\s*([^}\s]+)\s*}}/g)].map((match) => match[1]).sort();

describe('xAI presentation', () => {
  it('keeps every supported issue classification translated in every locale', () => {
    const issueKeys = XAI_PROBE_ISSUE_CLASSIFICATIONS.map((classification) => {
      const key = getXaiProbeIssueKey(classification);
      expect(key, classification).toBeTypeOf('string');
      return key as string;
    });
    const templateKeys = [
      'auth_files.provider_inspection_badge_error_title',
      'monitoring.codex_inspection_logs_filter_empty',
      'monitoring.codex_inspection_log_auth_files_failed',
      'monitoring.codex_inspection_log_cancelled',
      'monitoring.codex_inspection_log_manual_started',
      'monitoring.codex_inspection_log_action_skipped',
      'monitoring.codex_inspection_log_manual_completed',
      'monitoring.codex_inspection_log_result_write_failed',
      'monitoring.codex_inspection_log_result_write_retry',
      'monitoring.codex_inspection_log_ownership_failed',
      'monitoring.codex_inspection_log_manual_validation_failed',
      'monitoring.xai_inspection_log_result',
      'monitoring.xai_inspection_log_classified',
      'monitoring.xai_inspection_log_request_error',
      'monitoring.xai_inspection_surface_billing',
      'monitoring.xai_inspection_surface_inference',
      'monitoring.xai_inspection_evidence_billing_healthy',
      'monitoring.xai_inspection_evidence_billing_partial',
      'monitoring.xai_inspection_evidence_basic_healthy',
      'monitoring.xai_inspection_evidence_official_api_healthy',
      'monitoring.xai_inspection_evidence_inference_healthy',
      'monitoring.xai_inspection_log_server_complete',
      'monitoring.xai_inspection_log_server_missing_auth_index',
      'xai_quota.diagnostic_inference_quota_or_entitlement_unknown',
      'xai_quota.diagnostic_inference_probe_invalid',
      'xai_quota.diagnostic_inference_protocol_changed',
      'xai_quota.diagnostic_inference_request_error',
      'monitoring.server_codex_inspection_log_mode_billing',
      'monitoring.server_codex_inspection_log_mode_identity',
      'monitoring.server_codex_inspection_log_mode_inference',
      'monitoring.server_codex_inspection_log_mode_skipped',
      'monitoring.server_codex_inspection_log_message_run_started',
      'monitoring.server_codex_inspection_log_message_auth_files_failed',
      'monitoring.server_codex_inspection_log_message_set_ready',
      'monitoring.server_codex_inspection_log_message_cancelled',
      'monitoring.server_codex_inspection_log_message_completed',
      'monitoring.server_codex_inspection_log_message_manual_started',
      'monitoring.server_codex_inspection_log_message_manual_skipped',
      'monitoring.server_codex_inspection_log_message_manual_completed',
      'monitoring.server_codex_inspection_log_message_result_write_failed',
      'monitoring.server_codex_inspection_log_message_missing_auth_index',
      'monitoring.server_codex_inspection_log_message_probe_failed',
      'monitoring.server_codex_inspection_log_message_missing_status',
      'monitoring.server_codex_inspection_log_message_probe_completed',
      'monitoring.server_codex_inspection_log_message_auto_action_failed',
      'monitoring.server_codex_inspection_log_message_auto_action_succeeded',
      'monitoring.server_codex_inspection_log_message_auto_action_skipped',
      'monitoring.server_codex_inspection_log_message_auto_validation_failed',
      'monitoring.server_codex_inspection_log_message_manual_action_failed',
      'monitoring.server_codex_inspection_log_message_manual_action_succeeded',
      'monitoring.server_codex_inspection_log_message_ownership_failed',
      'monitoring.server_codex_inspection_log_message_manual_validation_failed',
    ];

    for (const key of [...issueKeys, ...templateKeys]) {
      const baseline = getPath(en, key);
      expect(baseline, `en:${key}`).toBeTypeOf('string');
      for (const [localeName, locale] of Object.entries(locales)) {
        const translated = getPath(locale, key);
        expect(translated, `${localeName}:${key}`).toBeTypeOf('string');
        expect(placeholders(String(translated)), `${localeName}:${key}`).toEqual(
          placeholders(String(baseline))
        );
      }
    }
  });

  it('uses inference-specific diagnostics when the inference surface is active', () => {
    const t = ((key: string) => key) as TFunction;

    expect(getXaiProbeIssueKey('protocol_changed', 'billing')).toBe(
      'xai_quota.diagnostic_protocol_changed'
    );
    expect(getXaiProbeIssueKey('protocol_changed', 'inference')).toBe(
      'xai_quota.diagnostic_inference_protocol_changed'
    );
    expect(getXaiProbeIssueKey('quota_or_entitlement_unknown', 'inference')).toBe(
      'xai_quota.diagnostic_inference_quota_or_entitlement_unknown'
    );
    expect(formatXaiProbeIssue('protocol_changed', t, 'inference')).toBe(
      'xai_quota.diagnostic_inference_protocol_changed'
    );

    expect(String(getPath(en, 'xai_quota.diagnostic_protocol_changed'))).toContain('billing');
    expect(String(getPath(en, 'xai_quota.diagnostic_inference_protocol_changed'))).not.toContain(
      'billing endpoint'
    );
  });

  it('uses a friendly fallback without dropping unknown partial diagnostics', () => {
    const messages: Record<string, string> = {
      'xai_quota.diagnostic_protocol_changed': 'Billing data format is not recognized',
      'xai_quota.diagnostic_unknown': 'The cause could not be determined',
      'xai_quota.partial_unknown': 'No diagnostic details are available',
    };
    const t = ((key: string) => messages[key] ?? key) as TFunction;

    expect(
      formatXaiBillingDiagnostics(
        [
          { classification: 'protocol_changed', statusCode: 200, message: 'schema changed' },
          { classification: 'future_xai_failure', statusCode: 503, message: 'future failure' },
        ],
        t
      )
    ).toBe('Billing data format is not recognized · The cause could not be determined');
  });

  it('directs client-version failures to upgrading CPA Manager Plus', () => {
    for (const [localeName, locale] of Object.entries(locales)) {
      expect(String(getPath(locale, 'xai_quota.diagnostic_client_outdated')), localeName).toContain(
        'CPA Manager Plus'
      );
      expect(
        String(getPath(locale, 'monitoring.xai_inspection_reason_client_outdated')),
        localeName
      ).toContain('CPA Manager Plus');
    }
  });
});
