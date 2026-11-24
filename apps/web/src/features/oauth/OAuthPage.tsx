import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { PageControlBar } from '@/components/common/PageControlBar';
import { Button } from '@/components/ui/Button';
import { IconChevronLeft, IconExternalLink, IconInfo, IconRefreshCw } from '@/components/ui/icons';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useThemeStore } from '@/stores';
import iconVertex from '@/assets/icons/vertex.svg';
import iconCodex from '@/assets/icons/codex.svg';
import { OAuthFlowDialog } from './components/OAuthFlowDialog';
import { ProviderTile, type ProviderTileStatus } from './components/ProviderTile';
import { SiwcBindDialog } from './components/SiwcBindDialog';
import { VertexImportDialog } from './components/VertexImportDialog';
import { resolveFlowView } from './flowView';
import { useOAuthFlows } from './hooks/useOAuthFlows';
import { useVertexImport } from './hooks/useVertexImport';
import { resolveThemedIcon, type OAuthProviderCard } from './providers';
import styles from './OAuthPage.module.scss';

const OAUTH_PROVIDER_HASH_PREFIX = '#oauth-provider-';

const parseTargetProviderFromHash = (hash: string): string | null => {
  if (!hash.startsWith(OAUTH_PROVIDER_HASH_PREFIX)) return null;
  try {
    const raw = decodeURIComponent(hash.slice(OAUTH_PROVIDER_HASH_PREFIX.length)).trim();
    return raw || null;
  } catch {
    return null;
  }
};

export function OAuthPage() {
  const { t } = useTranslation();
  const location = useLocation();
  const navigate = useNavigate();
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const theme = resolvedTheme === 'dark' ? 'dark' : 'light';

  const [focusedModal, setFocusedModal] = useState<{
    fingerprint: string | null;
    providerId: string;
  } | null>(null);
  const [vertexModalFingerprint, setVertexModalFingerprint] = useState<string | null | undefined>(
    undefined
  );
  const [siwcModalFingerprint, setSiwcModalFingerprint] = useState<string | null | undefined>(
    undefined
  );

  const focusedProviderRef = useRef<string | null>(null);
  const focusedModalTokenRef = useRef(0);
  const autoOpenedNavKeyRef = useRef<string | null>(null);
  const activeCycleIndexRef = useRef(-1);

  const isFocused = useCallback((provider: string) => focusedProviderRef.current === provider, []);

  const flows = useOAuthFlows({
    search: location.search,
    isFocused,
  });

  const focusedProviderId =
    focusedModal && focusedModal.fingerprint === flows.connectionFingerprint
      ? focusedModal.providerId
      : null;
  const vertexDialogOpen =
    vertexModalFingerprint !== undefined && vertexModalFingerprint === flows.connectionFingerprint;
  const siwcDialogOpen =
    siwcModalFingerprint !== undefined && siwcModalFingerprint === flows.connectionFingerprint;

  useLayoutEffect(() => {
    focusedProviderRef.current = focusedProviderId;
    focusedModalTokenRef.current += 1;
  }, [flows.connectionFingerprint, focusedProviderId]);

  const vertex = useVertexImport({
    connectionFingerprint: flows.connectionFingerprint,
    requestScope: flows.requestScope,
    onCredentialAdded: flows.recordCredentialAdded,
  });

  const connected = flows.connectionStatus === 'connected';
  const targetProviderId = useMemo(
    () => parseTargetProviderFromHash(location.hash),
    [location.hash]
  );

  useHeaderRefresh(flows.loadPluginProviders, flows.pluginOAuthAvailable);

  const { states: flowStates, startAuth, dismissSuccess, connectionFingerprint } = flows;

  const openProviderFlow = useCallback(
    (providerId: string) => {
      focusedModalTokenRef.current += 1;
      setFocusedModal({ fingerprint: connectionFingerprint, providerId });
      const currentState = flowStates[providerId] ?? {};
      const view = resolveFlowView(currentState);
      if (connected && (view === 'idle' || view === 'failed')) {
        void startAuth(providerId);
      }
    },
    [connected, connectionFingerprint, flowStates, startAuth]
  );

  const closeProviderFlow = useCallback(() => {
    focusedModalTokenRef.current += 1;
    if (focusedProviderId) {
      dismissSuccess(focusedProviderId);
    }
    setFocusedModal(null);
  }, [dismissSuccess, focusedProviderId]);

  // Deep-link handling for `/oauth?accountReauth=...#oauth-provider-<id>`
  useEffect(() => {
    if (!targetProviderId) return;
    const targetElement = document.getElementById(`oauth-provider-${targetProviderId}`);
    targetElement?.scrollIntoView({ behavior: 'smooth', block: 'center' });

    if (!connected) return;
    const navKey = `${location.key}:${location.search}:${location.hash}`;
    if (!flows.accountReauthSessionId || autoOpenedNavKeyRef.current === navKey) return;

    const matchedProvider = flows.providerCards.find((card) => card.id === targetProviderId);
    if (!matchedProvider) return;
    const rafId = window.requestAnimationFrame(() => {
      if (autoOpenedNavKeyRef.current === navKey) return;
      autoOpenedNavKeyRef.current = navKey;
      openProviderFlow(matchedProvider.id);
    });
    return () => window.cancelAnimationFrame(rafId);
  }, [
    connected,
    flows.accountReauthSessionId,
    flows.providerCards,
    location.hash,
    location.key,
    location.search,
    openProviderFlow,
    targetProviderId,
  ]);

  const focusedCard = useMemo(
    () => flows.providerCards.find((item) => item.id === focusedProviderId) ?? null,
    [flows.providerCards, focusedProviderId]
  );

  const targetProviderCard = useMemo(
    () => (targetProviderId ? flows.providerCards.find((item) => item.id === targetProviderId) ?? null : null),
    [flows.providerCards, targetProviderId]
  );

  const activeProviderCards = useMemo(
    () =>
      flows.providerCards.filter((card) => {
        const st = flows.states[card.id];
        return Boolean(st && resolveFlowView(st) === 'flow');
      }),
    [flows.providerCards, flows.states]
  );

  const openNextActiveFlow = useCallback(() => {
    if (activeProviderCards.length === 0) return;
    const nextIndex = (activeCycleIndexRef.current + 1) % activeProviderCards.length;
    activeCycleIndexRef.current = nextIndex;
    openProviderFlow(activeProviderCards[nextIndex].id);
  }, [activeProviderCards, openProviderFlow]);

  const { vertex: vertexState, resetForAnother: resetVertexForAnother } = vertex;
  const closeVertexDialog = useCallback(() => {
    if (vertexState.result) {
      resetVertexForAnother();
    }
    setVertexModalFingerprint(undefined);
  }, [resetVertexForAnother, vertexState.result]);
  const openSiwcDialog = useCallback(() => {
    setSiwcModalFingerprint(connectionFingerprint);
  }, [connectionFingerprint]);
  const closeSiwcDialog = useCallback(() => {
    setSiwcModalFingerprint(undefined);
  }, []);
  const totalChannels = flows.providerCards.length + 2; // Built-ins + Plugins + Vertex JSON + SIWC Bind

  const getTilePresentation = (
    card: OAuthProviderCard
  ): {
    title: string;
    subtitle: string;
    icon: string;
    status: ProviderTileStatus;
    statusText?: string;
  } => {
    const state = flows.states[card.id] ?? {};
    const view = resolveFlowView(state);
    const title =
      card.kind === 'builtin'
        ? typeof card.label === 'string'
          ? card.label
          : t(card.label.key)
        : card.title;
    const icon = card.kind === 'builtin' ? resolveThemedIcon(card.icon, theme) : card.icon;

    let subtitle: string;
    if (card.kind === 'plugin') {
      subtitle = t('auth_login.tile_subtitle_plugin');
    } else if (card.flow === 'device') {
      subtitle = card.domain
        ? `${t('auth_login.tile_subtitle_device')} · ${card.domain}`
        : t('auth_login.tile_subtitle_device');
    } else if (card.id === 'xai') {
      subtitle = t('auth_login.tile_subtitle_xai');
    } else {
      subtitle = t('auth_login.tile_subtitle_browser');
    }

    if (view === 'flow') {
      return {
        title,
        subtitle,
        icon,
        status: 'waiting',
        statusText: state.pollingError
          ? t('auth_login.tile_status_paused')
          : t('auth_login.tile_status_waiting'),
      };
    }
    if (view === 'success') {
      return {
        title,
        subtitle,
        icon,
        status: 'success',
        statusText: t('auth_login.tile_status_added'),
      };
    }
    if (view === 'failed') {
      return {
        title,
        subtitle,
        icon,
        status: 'error',
        statusText: t('auth_login.tile_status_error'),
      };
    }
    return { title, subtitle, icon, status: 'idle' };
  };

  const getCardLabel = (card: OAuthProviderCard): string =>
    card.kind === 'builtin'
      ? typeof card.label === 'string'
        ? card.label
        : t(card.label.key)
      : card.title;

  return (
    <div className={styles.container}>
      <PageControlBar
        leading={
          <div className={styles.controlSummary}>
            <span
              className={`${styles.connectionDot} ${connected ? styles.connectionDotLive : ''}`}
              aria-hidden="true"
            />
            <span className={styles.channelCount}>
              {t('auth_login.channels_count', { count: totalChannels })}
            </span>
            {activeProviderCards.length > 0 && (
              <button
                type="button"
                className={styles.activePill}
                title={activeProviderCards.map(getCardLabel).join(' / ')}
                onClick={openNextActiveFlow}
              >
                <span className={styles.statusDotWaiting} aria-hidden="true" />
                <span>
                  {t('auth_login.in_progress_count', { count: activeProviderCards.length })}
                </span>
              </button>
            )}
            {flows.addedCount > 0 && (
              <span className={styles.addedPill}>
                +{flows.addedCount} {t('auth_login.added_this_session')}
              </span>
            )}
          </div>
        }
        actions={
          <>
            {flows.pluginOAuthAvailable && (
              <Button
                variant="secondary"
                size="sm"
                onClick={() => void flows.loadPluginProviders()}
                loading={flows.pluginsLoading}
                disabled={flows.pluginsLoading}
              >
                <IconRefreshCw size={14} />
                <span>{t('auth_login.refresh_plugins')}</span>
              </Button>
            )}
            <Button variant="secondary" size="sm" onClick={() => navigate('/accounts')}>
              <span>{t('auth_login.view_credentials')}</span>
              <IconExternalLink size={14} />
            </Button>
          </>
        }
      />

      {flows.accountReauthSessionId && targetProviderCard && (
        <div className={styles.reauthBanner}>
          <div className={styles.reauthBannerText}>
            <span className={styles.statusDotWaiting} aria-hidden="true" />
            <span>
              {t('auth_login.reauth_banner_message', {
                provider: getCardLabel(targetProviderCard),
              })}
            </span>
          </div>
          <div className={styles.reauthBannerActions}>
            <Button
              variant="primary"
              size="sm"
              onClick={() => openProviderFlow(targetProviderCard.id)}
              disabled={!connected}
            >
              {t('auth_login.reauth_open_flow')}
            </Button>
            <Button variant="secondary" size="sm" onClick={() => navigate('/accounts')}>
              <IconChevronLeft size={14} />
              <span>{t('auth_login.return_to_accounts')}</span>
            </Button>
          </div>
        </div>
      )}

      {/* Main OAuth / Device-code Providers */}
      <section className={styles.sectionPanel} aria-labelledby="oauth-builtin-heading">
        <header className={styles.panelHeader}>
          <div className={styles.panelHeaderTitleGroup}>
            <h2 id="oauth-builtin-heading" className={styles.panelTitle}>
              {t('auth_login.oauth_section_title')}
            </h2>
            <span className={styles.panelCount}>{flows.builtInProviders.length}</span>
          </div>
          <span className={styles.panelMeta}>{t('auth_login.oauth_section_hint')}</span>
        </header>

        <div className={styles.panelBody}>
          <div className={styles.tileGrid}>
            {flows.builtInProviders.map((card) => {
              const tile = getTilePresentation(card);
              return (
                <ProviderTile
                  key={card.id}
                  domId={`oauth-provider-${card.id}`}
                  icon={tile.icon}
                  title={tile.title}
                  subtitle={tile.subtitle}
                  status={tile.status}
                  statusText={tile.statusText}
                  highlighted={targetProviderId === card.id}
                  disabled={!connected}
                  onClick={() => openProviderFlow(card.id)}
                />
              );
            })}
          </div>
        </div>
      </section>

      {/* Plugin OAuth Providers (rendered when enabled plugins exist or plugin fetch failed) */}
      {flows.pluginOAuthAvailable &&
        (flows.pluginProviders.length > 0 || Boolean(flows.pluginLoadError)) && (
          <section className={styles.sectionPanel} aria-labelledby="oauth-plugin-heading">
            <header className={styles.panelHeader}>
              <div className={styles.panelHeaderTitleGroup}>
                <h2 id="oauth-plugin-heading" className={styles.panelTitle}>
                  {t('auth_login.plugin_section_title')}
                </h2>
                <span className={styles.panelCount}>{flows.pluginProviders.length}</span>
              </div>
              <span className={styles.panelMeta}>{t('auth_login.plugin_section_hint')}</span>
            </header>

            <div className={styles.panelBody}>
              {flows.pluginLoadError ? (
                <div role="alert" className={styles.pluginErrorRow}>
                  <div className={styles.pluginErrorMessage}>
                    <IconInfo size={15} />
                    <span>{flows.pluginLoadError}</span>
                  </div>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => void flows.loadPluginProviders()}
                  >
                    {t('common.retry')}
                  </Button>
                </div>
              ) : (
                <div className={styles.tileGrid}>
                  {flows.pluginProviders.map((card) => {
                    const tile = getTilePresentation(card);
                    return (
                      <ProviderTile
                        key={card.id}
                        domId={`oauth-provider-${card.id}`}
                        icon={tile.icon}
                        title={tile.title}
                        subtitle={tile.subtitle}
                        status={tile.status}
                        statusText={tile.statusText}
                        highlighted={targetProviderId === card.id}
                        disabled={!connected}
                        onClick={() => openProviderFlow(card.id)}
                      />
                    );
                  })}
                </div>
              )}
            </div>
          </section>
        )}

      {/* Other Credential Methods (Vertex AI Import & SIWC Binding) */}
      <section className={styles.sectionPanel} aria-labelledby="oauth-other-methods-heading">
        <header className={styles.panelHeader}>
          <div className={styles.panelHeaderTitleGroup}>
            <h2 id="oauth-other-methods-heading" className={styles.panelTitle}>
              {t('auth_login.other_methods_section_title')}
            </h2>
            <span className={styles.panelCount}>2</span>
          </div>
          <span className={styles.panelMeta}>{t('auth_login.other_methods_section_hint')}</span>
        </header>

        <div className={styles.panelBody}>
          <div className={styles.tileGrid}>
            <ProviderTile
              domId="oauth-provider-vertex"
              icon={iconVertex}
              title={t('vertex_import.tile_title')}
              subtitle={t('auth_login.vertex_tile_subtitle')}
              status={vertex.vertex.result ? 'success' : vertex.vertex.error ? 'error' : 'idle'}
              statusText={
                vertex.vertex.result
                  ? t('auth_login.tile_status_added')
                  : vertex.vertex.error
                    ? t('auth_login.tile_status_error')
                    : undefined
              }
              highlighted={targetProviderId === 'vertex'}
              disabled={!connected}
              onClick={() => setVertexModalFingerprint(flows.connectionFingerprint)}
            />
            <ProviderTile
              domId="oauth-provider-siwc"
              icon={iconCodex}
              title={t('siwc.title')}
              subtitle={t('siwc.tile_caption')}
              status="idle"
              highlighted={targetProviderId === 'siwc'}
              disabled={!connected}
              onClick={openSiwcDialog}
            />
          </div>
        </div>
      </section>

      <OAuthFlowDialog
        card={focusedCard}
        state={focusedCard ? flows.states[focusedCard.id] ?? {} : {}}
        theme={theme}
        connected={connected}
        isReauthTarget={Boolean(
          focusedCard && flows.accountReauthSessionId && focusedCard.id === targetProviderId
        )}
        providerText={flows.providerText}
        onStart={() => {
          if (focusedCard) void flows.startAuth(focusedCard.id);
        }}
        onCancel={() => {
          if (!focusedCard) return;
          const providerId = focusedCard.id;
          const token = focusedModalTokenRef.current;
          void flows.cancelAuth(providerId).then((cancelled) => {
            if (
              cancelled &&
              focusedModalTokenRef.current === token &&
              focusedProviderRef.current === providerId
            ) {
              setFocusedModal(null);
            }
          });
        }}
        onResumePolling={() => {
          if (focusedCard) flows.resumePolling(focusedCard.id);
        }}
        onCallbackChange={(value) => {
          if (focusedCard) flows.setCallbackInput(focusedCard.id, value);
        }}
        onSubmitCallback={() => {
          if (focusedCard) void flows.submitCallback(focusedCard.id);
        }}
        onNavigateAccounts={() => navigate('/accounts')}
        onClose={closeProviderFlow}
      />

      <VertexImportDialog
        open={vertexDialogOpen}
        vertex={vertex.vertex}
        connected={connected}
        onPickFile={vertex.pickFile}
        onClearFile={vertex.clearFile}
        onLocationChange={vertex.setLocation}
        onImport={() => void vertex.importCredential()}
        onResetForAnother={vertex.resetForAnother}
        onNavigateAccounts={() => navigate('/accounts')}
        onClose={closeVertexDialog}
      />

      <SiwcBindDialog
        open={siwcDialogOpen}
        onClose={closeSiwcDialog}
        onSuccess={flows.recordCredentialAdded}
        onViewAccounts={() => {
          closeSiwcDialog();
          navigate('/accounts');
        }}
      />
    </div>
  );
}
