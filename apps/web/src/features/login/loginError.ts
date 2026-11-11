import type { TFunction } from 'i18next';
import { formatLocalizedText, type LocalizedText } from '@/i18n/localizedText';
import { getUsageServiceErrorCode } from '@/services/api/usageService';
import type { ApiError } from '@/types';

/**
 * A failed sign-in is shown until the next attempt, so it keeps the reason
 * instead of a translated sentence: the language menu above it must retranslate
 * what is already on screen.
 */
export type LoginError = LocalizedText & {
  /** Kept outside the sentence so the HTTP prefix survives translation. */
  httpStatus?: number;
  /** Raw backend text, appended verbatim after the localized prefix. */
  backendDetail?: string;
};

export const toLoginError = (error: unknown): LoginError => {
  const usageServiceCode = getUsageServiceErrorCode(error);
  if (usageServiceCode) {
    return {
      key: `usage_service_errors.${usageServiceCode}`,
      fallbackKey: 'usage_service_errors.request_failed',
    };
  }

  const apiError = error as Partial<ApiError>;
  const httpStatus = typeof apiError.status === 'number' ? apiError.status : undefined;
  const code = typeof apiError.code === 'string' ? apiError.code : undefined;
  const message =
    error instanceof Error
      ? error.message
      : typeof apiError.message === 'string'
        ? apiError.message
        : typeof error === 'string'
          ? error
          : '';
  const detail = message.trim();
  // Axios' generic status sentence repeats the code we already show.
  const backendDetail =
    httpStatus && detail && detail !== `Request failed with status code ${httpStatus}`
      ? detail
      : undefined;

  if (httpStatus === 401) return { key: 'login.error_unauthorized', httpStatus, backendDetail };
  if (httpStatus === 403) return { key: 'login.error_forbidden', httpStatus, backendDetail };
  if (httpStatus === 404) return { key: 'login.error_not_found', httpStatus, backendDetail };
  if (httpStatus && httpStatus >= 500) {
    return { key: 'login.error_server', httpStatus, backendDetail };
  }
  const lowered = message.toLowerCase();
  if (code === 'ECONNABORTED' || lowered.includes('timeout')) return { key: 'login.error_timeout' };
  if (code === 'ERR_NETWORK' || lowered.includes('network error')) {
    return { key: 'login.error_network' };
  }
  if (code === 'ERR_CERT_AUTHORITY_INVALID' || lowered.includes('certificate')) {
    return { key: 'login.error_ssl' };
  }
  if (lowered.includes('cors') || lowered.includes('cross-origin')) return { key: 'login.error_cors' };

  return { key: 'login.error_invalid', httpStatus, backendDetail };
};

export const formatLoginError = (error: LoginError, t: TFunction): string => {
  const summary = formatLocalizedText(error, t);
  const withStatus = error.httpStatus ? `HTTP ${error.httpStatus}: ${summary}` : summary;
  return error.backendDetail
    ? `${withStatus} (${t('login.error_backend_detail')}: ${error.backendDetail})`
    : withStatus;
};

/**
 * The failure toast repeats the error box above it, so it is built from the same
 * `LoginError`: a toast that is still on screen retranslates with the box instead
 * of keeping the language it was created in.
 */
export const formatLoginFailure = (error: LoginError, t: TFunction): string =>
  `${t('notification.login_failed')}: ${formatLoginError(error, t)}`;
