import type { TFunction } from 'i18next';
import { describe, expect, it, vi } from 'vitest';
import { buildProviderDeleteSecondConfirmation } from './deleteConfirmation';

describe('buildProviderDeleteSecondConfirmation', () => {
  it('keeps the provider and masked target visible in the final warning', () => {
    const t = vi.fn((key: string, values?: Record<string, unknown>) =>
      values ? `${key}:${values.provider}:${values.target}` : key
    ) as unknown as TFunction;

    const confirmation = buildProviderDeleteSecondConfirmation(t, 'Codex', 'sk******89');

    expect(confirmation).toEqual({
      title: 'ai_providers.delete_second_title',
      message: 'ai_providers.delete_second_confirm:Codex:sk******89',
      variant: 'danger',
      confirmText: 'ai_providers.delete_second_action',
    });
    expect(t).toHaveBeenCalledWith('ai_providers.delete_second_confirm', {
      provider: 'Codex',
      target: 'sk******89',
    });
  });
});
