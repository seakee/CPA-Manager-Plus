import type { TFunction } from 'i18next';

export const buildProviderDeleteSecondConfirmation = (
  t: TFunction,
  provider: string,
  target: string
) => ({
  title: t('ai_providers.delete_second_title'),
  message: t('ai_providers.delete_second_confirm', { provider, target }),
  variant: 'danger' as const,
  confirmText: t('ai_providers.delete_second_action'),
});
