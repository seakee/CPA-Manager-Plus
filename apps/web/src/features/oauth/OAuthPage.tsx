import { useCallback, useEffect, useMemo, useRef, useState, type ChangeEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useLocation, useNavigate } from 'react-router-dom';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { useAuthStore, useNotificationStore, useThemeStore } from '@/stores';
import {
  oauthApi,
  pluginsApi,
  type BuiltInOAuthProvider,
  type OAuthProvider,
} from '@/services/api';
import { vertexApi, type VertexImportResponse } from '@/services/api/vertex';
import { copyToClipboard } from '@/utils/clipboard';
import type { PluginListEntry } from '@/types';
import { getPluginTitle, resolvePluginAssetURL } from '@/features/plugins/pluginResources';
import {
  resolvePluginOAuthProviderId,
  shouldShowPluginOAuthProvider,
} from './oauthProviderHelpers';
import styles from './OAuthPage.module.scss';
import iconCodex from '@/assets/icons/codex.svg';
import iconClaude from '@/assets/icons/claude.svg';
import iconAntigravity from '@/assets/icons/antigravity.svg';
import iconKimiLight from '@/assets/icons/kimi-light.svg';
import iconKimiDark from '@/assets/icons/kimi-dark.svg';
import iconVertex from '@/assets/icons/vertex.svg';
import iconGrok from '@/assets/icons/grok.svg';
import iconGrokDark from '@/assets/icons/grok-dark.svg';

interface ProviderState {
  url?: string;
  state?: string;
  status?: 'idle' | 'waiting' | 'success' | 'error';
  error?: string;
  polling?: boolean;
  callbackUrl?: string;
  callbackSubmitting?: boolean;
  callbackStatus?: 'success' | 'error';
  callbackError?: string;
}

interface VertexImportResult {
  projectId?: string;
  email?: string;
  location?: string;
  authFile?: string;
}

interface VertexImportState {
  file?: File;
  fileName: string;
  location: string;
  loading: boolean;
  error?: string;
  result?: VertexImportResult;
}

interface BuiltInProviderDefinition {
  id: BuiltInOAuthProvider;
  titleKey: string;
  hintKey: string;
  urlLabelKey: string;
  icon: string | { light: string; dark: string };
}

interface OAuthProviderDefinition {
  id: OAuthProvider;
  title: string;
  hint: string;
  urlLabel: string;
  icon?: string | { light: string; dark: string };
  supportsCallback: boolean;
  isPlugin: boolean;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object';
}

function getErrorMessage(error: unknown): string {
  if (error instanceof Error) return error.message;
  if (isRecord(error) && typeof error.message === 'string') return error.message;
  return typeof error === 'string' ? error : '';
}

function getErrorStatus(error: unknown): number | undefined {
  if (!isRecord(error)) return undefined;
  return typeof error.status === 'number' ? error.status : undefined;
}

const BUILT_IN_PROVIDERS: BuiltInProviderDefinition[] = [
  {
    id: 'codex',
    titleKey: 'auth_login.codex_oauth_title',
    hintKey: 'auth_login.codex_oauth_hint',
    urlLabelKey: 'auth_login.codex_oauth_url_label',
    icon: iconCodex,
  },
  {
    id: 'anthropic',
    titleKey: 'auth_login.anthropic_oauth_title',
    hintKey: 'auth_login.anthropic_oauth_hint',
    urlLabelKey: 'auth_login.anthropic_oauth_url_label',
    icon: iconClaude,
  },
  {
    id: 'antigravity',
    titleKey: 'auth_login.antigravity_oauth_title',
    hintKey: 'auth_login.antigravity_oauth_hint',
    urlLabelKey: 'auth_login.antigravity_oauth_url_label',
    icon: iconAntigravity,
  },
  {
    id: 'kimi',
    titleKey: 'auth_login.kimi_oauth_title',
    hintKey: 'auth_login.kimi_oauth_hint',
    urlLabelKey: 'auth_login.kimi_oauth_url_label',
    icon: { light: iconKimiLight, dark: iconKimiDark },
  },
  {
    id: 'xai',
    titleKey: 'auth_login.xai_oauth_title',
    hintKey: 'auth_login.xai_oauth_hint',
    urlLabelKey: 'auth_login.xai_oauth_url_label',
    icon: { light: iconGrok, dark: iconGrokDark },
  },
];

const BUILT_IN_PROVIDER_IDS = new Set<string>(BUILT_IN_PROVIDERS.map((provider) => provider.id));

const CALLBACK_SUPPORTED = new Set<string>(['codex', 'anthropic', 'antigravity', 'xai']);
const XAI_CALLBACK_URL = 'http://127.0.0.1:56121/callback';
const SUCCESS_RESET_DELAY_MS = 5000;
const getProviderI18nPrefix = (provider: BuiltInOAuthProvider) => provider.replace('-', '_');
const getAuthKey = (provider: BuiltInOAuthProvider, suffix: string) =>
  `auth_login.${getProviderI18nPrefix(provider)}_${suffix}`;

const getIcon = (icon: string | { light: string; dark: string }, theme: 'light' | 'dark') => {
  return typeof icon === 'string' ? icon : icon[theme];
};

const isAbsoluteUrl = (value: string): boolean => {
  try {
    new URL(value);
    return true;
  } catch {
    return false;
  }
};

const readQueryLikeCallbackInput = (value: string) => {
  const trimmed = value.trim();
  if (!trimmed) return null;
  const queryStart = trimmed.indexOf('?');
  const hashStart = trimmed.indexOf('#');
  const rawParams =
    queryStart >= 0
      ? trimmed.slice(queryStart + 1)
      : hashStart >= 0
        ? trimmed.slice(hashStart + 1)
        : trimmed;

  if (!/(^|[&#?])(code|state|error)=/i.test(rawParams)) return null;
  return new URLSearchParams(rawParams.replace(/^[?#]/, ''));
};

const extractDisplayedXaiCode = (value: string): string => {
  const trimmed = value.trim();
  const codeMatch = trimmed.match(/\bcode\s*[:=]\s*([^\s&]+)/i);
  return (codeMatch?.[1] ?? trimmed).trim();
};

const buildXaiCallbackUrl = (input: string, state?: string): string | null => {
  const trimmed = input.trim();
  if (!trimmed) return null;
  if (isAbsoluteUrl(trimmed)) return trimmed;

  const params = readQueryLikeCallbackInput(trimmed);
  if (params) {
    const code = params.get('code')?.trim();
    const error = params.get('error')?.trim();
    const errorDescription = params.get('error_description')?.trim();
    const callbackState = params.get('state')?.trim() || state?.trim();
    if (!callbackState) return null;

    const callbackUrl = new URL(XAI_CALLBACK_URL);
    callbackUrl.searchParams.set('state', callbackState);
    if (code) callbackUrl.searchParams.set('code', code);
    if (error) callbackUrl.searchParams.set('error', error);
    if (errorDescription) callbackUrl.searchParams.set('error_description', errorDescription);
    return callbackUrl.toString();
  }

  const code = extractDisplayedXaiCode(trimmed);
  const callbackState = state?.trim();
  if (!code || !callbackState) return null;

  const callbackUrl = new URL(XAI_CALLBACK_URL);
  callbackUrl.searchParams.set('code', code);
  callbackUrl.searchParams.set('state', callbackState);
  return callbackUrl.toString();
};

const resolveCallbackUrl = (
  provider: OAuthProvider,
  input: string,
  state?: string
): string | null => {
  if (provider !== 'xai') return input.trim();
  return buildXaiCallbackUrl(input, state);
};

export function OAuthPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const { showNotification } = useNotificationStore();
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const apiBase = useAuthStore((state) => state.apiBase);
  const supportsPlugin = useAuthStore((state) => state.supportsPlugin);
  const pluginOAuthAvailable = connectionStatus === 'connected' && supportsPlugin;
  const [states, setStates] = useState<Record<string, ProviderState>>({});
  const [pluginOAuthPlugins, setPluginOAuthPlugins] = useState<PluginListEntry[]>([]);
  const [vertexState, setVertexState] = useState<VertexImportState>({
    fileName: '',
    location: '',
    loading: false,
  });
  const pollingTimers = useRef<Partial<Record<string, number>>>({});
  const successResetTimers = useRef<Partial<Record<string, number>>>({});
  const vertexFileInputRef = useRef<HTMLInputElement | null>(null);

  const providers = useMemo<OAuthProviderDefinition[]>(() => {
    const builtIn = BUILT_IN_PROVIDERS.map((provider) => ({
      id: provider.id,
      title: t(provider.titleKey),
      hint: t(provider.hintKey),
      urlLabel: t(provider.urlLabelKey),
      icon: provider.icon,
      supportsCallback: CALLBACK_SUPPORTED.has(provider.id),
      isPlugin: false,
    }));
    const pluginProviders = pluginOAuthAvailable
      ? pluginOAuthPlugins
          .filter((plugin) => shouldShowPluginOAuthProvider(plugin, BUILT_IN_PROVIDER_IDS))
          .map((plugin) => {
            const title = getPluginTitle(plugin);
            const logo = resolvePluginAssetURL(plugin.logo || plugin.metadata?.logo || '', apiBase);
            return {
              id: resolvePluginOAuthProviderId(plugin),
              title,
              hint: t('auth_login.plugin_oauth_hint', { plugin: title }),
              urlLabel: t('auth_login.plugin_oauth_url_label'),
              icon: logo || undefined,
              supportsCallback: false,
              isPlugin: true,
            };
          })
      : [];
    return [...builtIn, ...pluginProviders];
  }, [apiBase, pluginOAuthAvailable, pluginOAuthPlugins, t]);

  useEffect(() => {
    const targetId = location.hash.replace(/^#/, '');
    if (!targetId) return;
    const frame = window.requestAnimationFrame(() => {
      document.getElementById(targetId)?.scrollIntoView({ behavior: 'smooth', block: 'center' });
    });
    return () => window.cancelAnimationFrame(frame);
  }, [location.hash, providers.length]);

  const clearTimers = useCallback(() => {
    Object.values(pollingTimers.current).forEach((timer) => {
      if (timer !== undefined) window.clearInterval(timer);
    });
    Object.values(successResetTimers.current).forEach((timer) => {
      if (timer !== undefined) window.clearTimeout(timer);
    });
    pollingTimers.current = {};
    successResetTimers.current = {};
  }, []);

  useEffect(() => {
    return () => {
      clearTimers();
    };
  }, [clearTimers]);

  useEffect(() => {
    if (!pluginOAuthAvailable) return;

    let cancelled = false;
    pluginsApi
      .list()
      .then((response) => {
        if (cancelled) return;
        setPluginOAuthPlugins(response.plugins.filter((plugin) => plugin.supportsOAuth));
      })
      .catch(() => {
        if (cancelled) return;
        setPluginOAuthPlugins([]);
      });

    return () => {
      cancelled = true;
    };
  }, [apiBase, pluginOAuthAvailable]);

  const getProviderDefinition = useCallback(
    (provider: OAuthProvider) => providers.find((item) => item.id === provider),
    [providers]
  );

  const getProviderActionText = useCallback(
    (provider: OAuthProvider, suffix: string) => {
      const definition = getProviderDefinition(provider);
      if (!definition?.isPlugin && BUILT_IN_PROVIDER_IDS.has(provider)) {
        return t(getAuthKey(provider as BuiltInOAuthProvider, suffix));
      }
      return t(`auth_login.plugin_${suffix}`, {
        plugin: definition?.title || provider,
      });
    },
    [getProviderDefinition, t]
  );

  const updateProviderState = (provider: OAuthProvider, next: Partial<ProviderState>) => {
    setStates((prev) => ({
      ...prev,
      [provider]: { ...(prev[provider] ?? {}), ...next },
    }));
  };

  const clearPollingTimer = (provider: OAuthProvider) => {
    const timer = pollingTimers.current[provider];
    if (timer !== undefined) {
      window.clearInterval(timer);
      delete pollingTimers.current[provider];
    }
  };

  const clearSuccessResetTimer = (provider: OAuthProvider) => {
    const timer = successResetTimers.current[provider];
    if (timer !== undefined) {
      window.clearTimeout(timer);
      delete successResetTimers.current[provider];
    }
  };

  const clearProviderTimers = (provider: OAuthProvider) => {
    clearPollingTimer(provider);
    clearSuccessResetTimer(provider);
  };

  const resetProviderAttempt = (provider: OAuthProvider) => {
    clearProviderTimers(provider);
    setStates((prev) => {
      return {
        ...prev,
        [provider]: {},
      };
    });
  };

  const completeProviderAuth = (provider: OAuthProvider) => {
    clearPollingTimer(provider);
    clearSuccessResetTimer(provider);
    updateProviderState(provider, {
      url: undefined,
      state: undefined,
      status: 'success',
      error: undefined,
      polling: false,
      callbackUrl: '',
      callbackSubmitting: false,
      callbackStatus: undefined,
      callbackError: undefined,
    });
    successResetTimers.current[provider] = window.setTimeout(() => {
      resetProviderAttempt(provider);
    }, SUCCESS_RESET_DELAY_MS);
  };

  const startPolling = (provider: OAuthProvider, state: string) => {
    clearPollingTimer(provider);
    const timer = window.setInterval(async () => {
      try {
        const res = await oauthApi.getAuthStatus(state);
        if (res.status === 'ok') {
          completeProviderAuth(provider);
          showNotification(getProviderActionText(provider, 'oauth_status_success'), 'success');
        } else if (res.status === 'error') {
          updateProviderState(provider, { status: 'error', error: res.error, polling: false });
          showNotification(
            `${getProviderActionText(provider, 'oauth_status_error')} ${res.error || ''}`,
            'error'
          );
          window.clearInterval(timer);
          delete pollingTimers.current[provider];
        }
      } catch (err: unknown) {
        updateProviderState(provider, {
          status: 'error',
          error: getErrorMessage(err),
          polling: false,
        });
        window.clearInterval(timer);
        delete pollingTimers.current[provider];
      }
    }, 3000);
    pollingTimers.current[provider] = timer;
  };

  const startAuth = async (provider: OAuthProvider) => {
    clearProviderTimers(provider);
    updateProviderState(provider, {
      url: undefined,
      state: undefined,
      status: 'waiting',
      polling: true,
      error: undefined,
      callbackStatus: undefined,
      callbackError: undefined,
      callbackUrl: '',
    });
    try {
      const res = await oauthApi.startAuth(provider);
      if (!res.state) {
        const message = t('auth_login.missing_state');
        updateProviderState(provider, {
          url: res.url,
          state: undefined,
          status: 'error',
          error: message,
          polling: false,
        });
        showNotification(message, 'error');
        return;
      }
      updateProviderState(provider, {
        url: res.url,
        state: res.state,
        status: 'waiting',
        polling: true,
      });
      startPolling(provider, res.state);
    } catch (err: unknown) {
      const message = getErrorMessage(err);
      updateProviderState(provider, { status: 'error', error: message, polling: false });
      showNotification(
        `${getProviderActionText(provider, 'oauth_start_error')}${message ? ` ${message}` : ''}`,
        'error'
      );
    }
  };

  const copyLink = async (url?: string) => {
    if (!url) return;
    const copied = await copyToClipboard(url);
    showNotification(
      t(copied ? 'notification.link_copied' : 'notification.copy_failed'),
      copied ? 'success' : 'error'
    );
  };

  const submitCallback = async (provider: OAuthProvider) => {
    const callbackInput = (states[provider]?.callbackUrl || '').trim();
    if (!callbackInput) {
      showNotification(
        t(
          provider === 'xai'
            ? 'auth_login.xai_callback_required'
            : 'auth_login.oauth_callback_required'
        ),
        'warning'
      );
      return;
    }
    const redirectUrl = resolveCallbackUrl(provider, callbackInput, states[provider]?.state);
    if (!redirectUrl) {
      showNotification(
        t(
          provider === 'xai' ? 'auth_login.xai_callback_state_missing' : 'auth_login.missing_state'
        ),
        'warning'
      );
      return;
    }
    updateProviderState(provider, {
      callbackSubmitting: true,
      callbackStatus: undefined,
      callbackError: undefined,
    });
    try {
      await oauthApi.submitCallback(provider, redirectUrl);
      updateProviderState(provider, { callbackSubmitting: false, callbackStatus: 'success' });
      showNotification(t('auth_login.oauth_callback_success'), 'success');
    } catch (err: unknown) {
      const status = getErrorStatus(err);
      const message = getErrorMessage(err);
      const errorMessage =
        status === 404
          ? t('auth_login.oauth_callback_upgrade_hint', {
              defaultValue: 'Please update CLI Proxy API or check the connection.',
            })
          : message || undefined;
      updateProviderState(provider, {
        callbackSubmitting: false,
        callbackStatus: 'error',
        callbackError: errorMessage,
      });
      const notificationMessage = errorMessage
        ? `${t('auth_login.oauth_callback_error')} ${errorMessage}`
        : t('auth_login.oauth_callback_error');
      showNotification(notificationMessage, 'error');
    }
  };

  const handleVertexFilePick = () => {
    vertexFileInputRef.current?.click();
  };

  const handleVertexFileChange = (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    if (!file) return;
    if (!file.name.endsWith('.json')) {
      showNotification(t('vertex_import.file_required'), 'warning');
      event.target.value = '';
      return;
    }
    setVertexState((prev) => ({
      ...prev,
      file,
      fileName: file.name,
      error: undefined,
      result: undefined,
    }));
    event.target.value = '';
  };

  const handleVertexImport = async () => {
    if (!vertexState.file) {
      const message = t('vertex_import.file_required');
      setVertexState((prev) => ({ ...prev, error: message }));
      showNotification(message, 'warning');
      return;
    }
    const location = vertexState.location.trim();
    setVertexState((prev) => ({ ...prev, loading: true, error: undefined, result: undefined }));
    try {
      const res: VertexImportResponse = await vertexApi.importCredential(
        vertexState.file,
        location || undefined
      );
      const result: VertexImportResult = {
        projectId: res.project_id,
        email: res.email,
        location: res.location,
        authFile: res['auth-file'] ?? res.auth_file,
      };
      setVertexState((prev) => ({ ...prev, loading: false, result }));
      showNotification(t('vertex_import.success'), 'success');
    } catch (err: unknown) {
      const message = getErrorMessage(err);
      setVertexState((prev) => ({
        ...prev,
        loading: false,
        error: message || t('notification.upload_failed'),
      }));
      const notification = message
        ? `${t('notification.upload_failed')}: ${message}`
        : t('notification.upload_failed');
      showNotification(notification, 'error');
    }
  };

  return (
    <div className={styles.container}>
      <div className={styles.content}>
        {providers.map((provider) => {
          const state = states[provider.id] || {};
          const canSubmitCallback = provider.supportsCallback && Boolean(state.url);
          const loginButtonLabel =
            state.status === 'success'
              ? t('auth_login.login_another_account')
              : getProviderActionText(provider.id, 'oauth_button');
          const statusBadgeClassName = [
            'status-badge',
            state.status === 'success' ? 'success' : '',
            state.status === 'error' ? 'error' : '',
          ]
            .filter(Boolean)
            .join(' ');
          return (
            <div key={provider.id} id={`oauth-provider-${provider.id}`}>
              <Card
                title={
                  <span className={styles.cardTitle}>
                    {provider.icon ? (
                      <img
                        src={getIcon(provider.icon, resolvedTheme)}
                        alt=""
                        className={styles.cardTitleIcon}
                      />
                    ) : (
                      <span className={styles.pluginIconFallback} aria-hidden="true">
                        {provider.title.slice(0, 1).toUpperCase()}
                      </span>
                    )}
                    {provider.title}
                  </span>
                }
                extra={
                  <Button onClick={() => startAuth(provider.id)} loading={state.polling}>
                    {loginButtonLabel}
                  </Button>
                }
              >
                <div className={styles.cardContent}>
                  <div className={styles.cardHint}>{provider.hint}</div>
                  {state.url && (
                    <div className={styles.authUrlBox}>
                      <div className={styles.authUrlLabel}>{provider.urlLabel}</div>
                      <div className={styles.authUrlValue}>{state.url}</div>
                      <div className={styles.authUrlActions}>
                        <Button variant="secondary" size="sm" onClick={() => copyLink(state.url!)}>
                          {getProviderActionText(provider.id, 'copy_link')}
                        </Button>
                        <Button
                          variant="secondary"
                          size="sm"
                          onClick={() => window.open(state.url, '_blank', 'noopener,noreferrer')}
                        >
                          {getProviderActionText(provider.id, 'open_link')}
                        </Button>
                      </div>
                    </div>
                  )}
                  {canSubmitCallback && (
                    <div className={styles.callbackSection}>
                      <Input
                        label={t(
                          provider.id === 'xai'
                            ? 'auth_login.xai_callback_label'
                            : 'auth_login.oauth_callback_label'
                        )}
                        hint={t(
                          provider.id === 'xai'
                            ? 'auth_login.xai_callback_hint'
                            : 'auth_login.oauth_callback_hint'
                        )}
                        value={state.callbackUrl || ''}
                        onChange={(e) =>
                          updateProviderState(provider.id, {
                            callbackUrl: e.target.value,
                            callbackStatus: undefined,
                            callbackError: undefined,
                          })
                        }
                        placeholder={t(
                          provider.id === 'xai'
                            ? 'auth_login.xai_callback_placeholder'
                            : 'auth_login.oauth_callback_placeholder'
                        )}
                      />
                      <div className={styles.callbackActions}>
                        <Button
                          variant="secondary"
                          size="sm"
                          onClick={() => submitCallback(provider.id)}
                          loading={state.callbackSubmitting}
                        >
                          {t('auth_login.oauth_callback_button')}
                        </Button>
                      </div>
                      {state.callbackStatus === 'success' && state.status === 'waiting' && (
                        <div className="status-badge success">
                          {t('auth_login.oauth_callback_status_success')}
                        </div>
                      )}
                      {state.callbackStatus === 'error' && (
                        <div className="status-badge error">
                          {t('auth_login.oauth_callback_status_error')} {state.callbackError || ''}
                        </div>
                      )}
                    </div>
                  )}
                  {state.status && state.status !== 'idle' && (
                    <div className={statusBadgeClassName}>
                      {state.status === 'success'
                        ? getProviderActionText(provider.id, 'oauth_status_success')
                        : state.status === 'error'
                          ? `${getProviderActionText(provider.id, 'oauth_status_error')} ${state.error || ''}`
                          : getProviderActionText(provider.id, 'oauth_status_waiting')}
                    </div>
                  )}
                  {state.status === 'success' && (
                    <div className={styles.successActions}>
                      <Button variant="secondary" size="sm" onClick={() => navigate('/auth-files')}>
                        {t('auth_login.view_auth_files')}
                      </Button>
                    </div>
                  )}
                </div>
              </Card>
            </div>
          );
        })}

        {/* Vertex JSON 登录 */}
        <Card
          title={
            <span className={styles.cardTitle}>
              <img src={iconVertex} alt="" className={styles.cardTitleIcon} />
              {t('vertex_import.title')}
            </span>
          }
          extra={
            <Button onClick={handleVertexImport} loading={vertexState.loading}>
              {t('vertex_import.import_button')}
            </Button>
          }
        >
          <div className={styles.cardContent}>
            <div className={styles.cardHint}>{t('vertex_import.description')}</div>
            <Input
              label={t('vertex_import.location_label')}
              hint={t('vertex_import.location_hint')}
              value={vertexState.location}
              onChange={(e) =>
                setVertexState((prev) => ({
                  ...prev,
                  location: e.target.value,
                }))
              }
              placeholder={t('vertex_import.location_placeholder')}
            />
            <div className={styles.formItem}>
              <label className={styles.formItemLabel}>{t('vertex_import.file_label')}</label>
              <div className={styles.filePicker}>
                <Button variant="secondary" size="sm" onClick={handleVertexFilePick}>
                  {t('vertex_import.choose_file')}
                </Button>
                <div
                  className={`${styles.fileName} ${
                    vertexState.fileName ? '' : styles.fileNamePlaceholder
                  }`.trim()}
                >
                  {vertexState.fileName || t('vertex_import.file_placeholder')}
                </div>
              </div>
              <div className={styles.cardHintSecondary}>{t('vertex_import.file_hint')}</div>
              <input
                ref={vertexFileInputRef}
                type="file"
                accept=".json,application/json"
                style={{ display: 'none' }}
                onChange={handleVertexFileChange}
              />
            </div>
            {vertexState.error && <div className="status-badge error">{vertexState.error}</div>}
            {vertexState.result && (
              <div className={styles.connectionBox}>
                <div className={styles.connectionLabel}>{t('vertex_import.result_title')}</div>
                <div className={styles.keyValueList}>
                  {vertexState.result.projectId && (
                    <div className={styles.keyValueItem}>
                      <span className={styles.keyValueKey}>
                        {t('vertex_import.result_project')}
                      </span>
                      <span className={styles.keyValueValue}>{vertexState.result.projectId}</span>
                    </div>
                  )}
                  {vertexState.result.email && (
                    <div className={styles.keyValueItem}>
                      <span className={styles.keyValueKey}>{t('vertex_import.result_email')}</span>
                      <span className={styles.keyValueValue}>{vertexState.result.email}</span>
                    </div>
                  )}
                  {vertexState.result.location && (
                    <div className={styles.keyValueItem}>
                      <span className={styles.keyValueKey}>
                        {t('vertex_import.result_location')}
                      </span>
                      <span className={styles.keyValueValue}>{vertexState.result.location}</span>
                    </div>
                  )}
                  {vertexState.result.authFile && (
                    <div className={styles.keyValueItem}>
                      <span className={styles.keyValueKey}>{t('vertex_import.result_file')}</span>
                      <span className={styles.keyValueValue}>{vertexState.result.authFile}</span>
                    </div>
                  )}
                </div>
              </div>
            )}
          </div>
        </Card>
      </div>
    </div>
  );
}
