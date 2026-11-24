import type { BuiltInOAuthProvider } from '@/services/api';
import type { PluginListEntry } from '@/types';
import { getPluginTitle, resolvePluginAssetURL } from '@/utils/pluginAssets';
import iconCodex from '@/assets/icons/codex.svg';
import iconClaude from '@/assets/icons/claude.svg';
import iconAntigravity from '@/assets/icons/antigravity.svg';
import iconKimiLight from '@/assets/icons/kimi-light.svg';
import iconKimiDark from '@/assets/icons/kimi-dark.svg';
import iconGrok from '@/assets/icons/grok.svg';
import iconGrokDark from '@/assets/icons/grok-dark.svg';
import iconDevin from '@/assets/icons/devin.svg';
import iconDevinDark from '@/assets/icons/devin-dark.svg';
import iconMeta from '@/assets/icons/meta.svg';

export type ThemedIcon = string | { light: string; dark: string };
export type OAuthFlowKind = 'browser' | 'device';

export interface BuiltInOAuthProviderCard {
  kind: 'builtin';
  id: BuiltInOAuthProvider;
  label: string | { key: string };
  titleKey: string;
  hintKey: string;
  urlLabelKey: string;
  icon: ThemedIcon;
  flow: OAuthFlowKind;
  domain?: string;
  supportsCallback: boolean;
}

export interface PluginOAuthProviderCard {
  kind: 'plugin';
  id: string;
  pluginId: string;
  title: string;
  icon: string;
  flow: OAuthFlowKind;
  supportsCallback: boolean;
}

export type OAuthProviderCard = BuiltInOAuthProviderCard | PluginOAuthProviderCard;

export const BUILT_IN_OAUTH_PROVIDERS: BuiltInOAuthProviderCard[] = [
  {
    kind: 'builtin',
    id: 'codex',
    label: 'Codex',
    titleKey: 'auth_login.codex_oauth_title',
    hintKey: 'auth_login.codex_oauth_hint',
    urlLabelKey: 'auth_login.codex_oauth_url_label',
    icon: iconCodex,
    flow: 'browser',
    supportsCallback: true,
  },
  {
    kind: 'builtin',
    id: 'anthropic',
    label: 'Claude',
    titleKey: 'auth_login.anthropic_oauth_title',
    hintKey: 'auth_login.anthropic_oauth_hint',
    urlLabelKey: 'auth_login.anthropic_oauth_url_label',
    icon: iconClaude,
    flow: 'browser',
    supportsCallback: true,
  },
  {
    kind: 'builtin',
    id: 'antigravity',
    label: 'Antigravity',
    titleKey: 'auth_login.antigravity_oauth_title',
    hintKey: 'auth_login.antigravity_oauth_hint',
    urlLabelKey: 'auth_login.antigravity_oauth_url_label',
    icon: iconAntigravity,
    flow: 'browser',
    supportsCallback: true,
  },
  {
    kind: 'builtin',
    id: 'kimi',
    label: { key: 'auth_login.kimi_oauth_name' },
    titleKey: 'auth_login.kimi_oauth_title',
    hintKey: 'auth_login.kimi_oauth_hint',
    urlLabelKey: 'auth_login.kimi_oauth_url_label',
    icon: { light: iconKimiLight, dark: iconKimiDark },
    flow: 'device',
    domain: 'kimi.com',
    supportsCallback: false,
  },
  {
    kind: 'builtin',
    id: 'kimi-ai',
    label: { key: 'auth_login.kimi_ai_oauth_name' },
    titleKey: 'auth_login.kimi_ai_oauth_title',
    hintKey: 'auth_login.kimi_ai_oauth_hint',
    urlLabelKey: 'auth_login.kimi_ai_oauth_url_label',
    icon: { light: iconKimiLight, dark: iconKimiDark },
    flow: 'device',
    domain: 'kimi.ai',
    supportsCallback: false,
  },
  {
    kind: 'builtin',
    id: 'xai',
    label: 'xAI',
    titleKey: 'auth_login.xai_oauth_title',
    hintKey: 'auth_login.xai_oauth_hint',
    urlLabelKey: 'auth_login.xai_oauth_url_label',
    icon: { light: iconGrok, dark: iconGrokDark },
    flow: 'browser',
    supportsCallback: true,
  },
  {
    kind: 'builtin',
    id: 'devin',
    label: 'Devin',
    titleKey: 'auth_login.devin_oauth_title',
    hintKey: 'auth_login.devin_oauth_hint',
    urlLabelKey: 'auth_login.devin_oauth_url_label',
    icon: { light: iconDevin, dark: iconDevinDark },
    flow: 'browser',
    supportsCallback: true,
  },
  {
    kind: 'builtin',
    id: 'meta',
    label: 'Muse (Meta)',
    titleKey: 'auth_login.meta_oauth_title',
    hintKey: 'auth_login.meta_oauth_hint',
    urlLabelKey: 'auth_login.meta_oauth_url_label',
    icon: iconMeta,
    flow: 'device',
    supportsCallback: false,
  },
];

export const BUILT_IN_OAUTH_PROVIDER_MAP: Record<string, true> = Object.fromEntries(
  BUILT_IN_OAUTH_PROVIDERS.map((provider) => [provider.id, true])
);

export const resolveThemedIcon = (icon: ThemedIcon, theme: 'light' | 'dark'): string =>
  typeof icon === 'string' ? icon : icon[theme];

export const getBuiltInProviderI18nKey = (provider: string, suffix: string): string =>
  `auth_login.${provider.replace(/-/g, '_')}_${suffix}`;

export const buildPluginOAuthProviderCards = (
  plugins: PluginListEntry[],
  apiBase: string
): PluginOAuthProviderCard[] => {
  const seenProviders = new Set<string>(Object.keys(BUILT_IN_OAUTH_PROVIDER_MAP));

  return plugins.flatMap((plugin) => {
    if (!plugin.supportsOAuth || plugin.effectiveEnabled === false) return [];

    const declared = plugin.oauthProviders?.length
      ? plugin.oauthProviders
      : plugin.oauthProvider
        ? [plugin.oauthProvider]
        : [];
    const pluginTitle = getPluginTitle(plugin);
    const pluginLogo = plugin.logo || plugin.metadata?.logo || '';

    return declared.flatMap((provider) => {
      const normalizedId = provider.trim();
      if (!normalizedId || seenProviders.has(normalizedId)) return [];
      seenProviders.add(normalizedId);

      const entry = plugin.oauthProviderEntries?.find((item) => item.identifier === normalizedId);
      const rawLogo = entry?.logo?.trim() || pluginLogo;

      return [
        {
          kind: 'plugin' as const,
          id: normalizedId,
          pluginId: plugin.id,
          title: entry?.name?.trim() || pluginTitle,
          icon: rawLogo ? resolvePluginAssetURL(rawLogo, apiBase) : '',
          flow: 'browser' as const,
          supportsCallback: true,
        },
      ];
    });
  });
};
