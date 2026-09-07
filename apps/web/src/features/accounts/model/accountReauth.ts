import type { AuthFileItem } from '@/types';
import { normalizeAccountProvider } from './accountRows';
import { buildAccountOAuthReauthPath } from './accountReauthSession';

export type AccountReauthAction =
  | { kind: 'codex-dialog' }
  | { kind: 'navigate'; oauthProvider: string; path: string }
  | { kind: 'unsupported'; provider: string };

const OAUTH_PROVIDER_BY_ACCOUNT_PROVIDER: Record<string, string> = {
  anthropic: 'anthropic',
  antigravity: 'antigravity',
  claude: 'anthropic',
  kimi: 'kimi',
  xai: 'xai',
  qoder: 'qoder',
};

export const resolveAccountReauthAction = (file: AuthFileItem): AccountReauthAction => {
  const provider = normalizeAccountProvider(file);
  if (provider === 'codex') return { kind: 'codex-dialog' };

  const oauthProvider = OAUTH_PROVIDER_BY_ACCOUNT_PROVIDER[provider];
  if (oauthProvider) {
    return {
      kind: 'navigate',
      oauthProvider,
      path: buildAccountOAuthReauthPath(oauthProvider),
    };
  }

  return { kind: 'unsupported', provider };
};
