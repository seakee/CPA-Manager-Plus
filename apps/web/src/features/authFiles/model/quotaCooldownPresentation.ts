import type { QuotaCooldownInfo } from '@/services/api/usageService';

export const QUOTA_COOLDOWN_OWNER_CODEX_USAGE = 'cpamp_usage_429';
export const QUOTA_COOLDOWN_OWNER_XAI_FREE_USAGE = 'cpamp_xai_free_usage';

export type QuotaCooldownPresentationKind = 'codex' | 'xai' | 'provider';

export interface QuotaCooldownPresentation {
  kind: QuotaCooldownPresentationKind;
  badgeKey: string;
  badgeDefault: string;
  titleKey: string;
  titleDefault: string;
  providerLabel: string;
  sourceLabelKey: string;
  sourceLabelDefault: string;
}

const normalizedProvider = (value?: string): string => value?.trim().toLowerCase() ?? '';

export function getQuotaCooldownPresentation(
  cooldown: QuotaCooldownInfo
): QuotaCooldownPresentation {
  const owner = cooldown.owner?.trim() ?? '';
  const provider = normalizedProvider(cooldown.provider);

  if (owner === QUOTA_COOLDOWN_OWNER_XAI_FREE_USAGE || (!owner && provider === 'xai')) {
    return {
      kind: 'xai',
      badgeKey: 'auth_files.quota_cooldown_badge_xai',
      badgeDefault: 'xAI auto cooldown until {{recoverAt}}',
      titleKey: 'auth_files.quota_cooldown_badge_title_xai',
      titleDefault:
        'A real xAI request reported included free usage exhaustion. Usage evidence: {{usage}}. CPAMP temporarily disabled this credential and plans to restore it at {{recoverAt}} ({{recoveryKind}}). Disabled at: {{disabledAt}}. This is event-driven credential automation, not an active inspection.',
      providerLabel: 'xAI',
      sourceLabelKey: 'auth_files.quota_cooldown_source_xai',
      sourceLabelDefault: 'xAI free-usage exhaustion request event',
    };
  }

  if (owner === QUOTA_COOLDOWN_OWNER_CODEX_USAGE || (!owner && provider === 'codex')) {
    return {
      kind: 'codex',
      badgeKey: 'auth_files.quota_cooldown_badge_codex',
      badgeDefault: 'Codex auto cooldown until {{recoverAt}}',
      titleKey: 'auth_files.quota_cooldown_badge_title_codex',
      titleDefault:
        'A real Codex request reported an explicit usage limit. CPAMP temporarily disabled this credential and plans to restore it at the reported reset time, {{recoverAt}}. Disabled at: {{disabledAt}}. This is event-driven credential automation, not an active inspection.',
      providerLabel: 'Codex',
      sourceLabelKey: 'auth_files.quota_cooldown_source_codex',
      sourceLabelDefault: 'Codex usage-limit request event',
    };
  }

  return {
    kind: 'provider',
    badgeKey: 'auth_files.quota_cooldown_badge_provider',
    badgeDefault: 'Auto cooldown until {{recoverAt}}',
    titleKey: 'auth_files.quota_cooldown_badge_title_provider',
    titleDefault:
      'CPAMP temporarily disabled this credential after a supported provider quota event and plans to restore it at {{recoverAt}}. Disabled at: {{disabledAt}}. Owner: {{owner}}. This is event-driven credential automation, not an active inspection.',
    providerLabel: cooldown.provider?.trim() || 'Provider',
    sourceLabelKey: 'auth_files.quota_cooldown_source_provider',
    sourceLabelDefault: 'Supported provider quota request event',
  };
}
