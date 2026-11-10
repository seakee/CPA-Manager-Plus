import type { TFunction } from 'i18next';
import { formatLocalizedText, type LocalizedText } from '@/i18n/localizedText';

/**
 * The model-list status is fetched once and stays on screen while the user can
 * still switch language, so it keeps keys instead of a rendered sentence and is
 * formatted when it is shown.
 */
export interface ModelStatus {
  type: 'success' | 'warning' | 'error' | 'muted';
  message: LocalizedText;
  detail?: LocalizedText;
}

export const formatModelStatus = (status: ModelStatus, t: TFunction): string => {
  const message = formatLocalizedText(status.message, t);
  return status.detail ? `${message}: ${formatLocalizedText(status.detail, t)}` : message;
};
