import { useCallback, useEffect, useId, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { Modal } from '@/components/ui/Modal';
import {
  IconCheck,
  IconCopy,
  IconExternalLink,
  IconInfo,
  IconKey,
  IconRefreshCw,
} from '@/components/ui/icons';
import { copyToClipboard } from '@/utils/clipboard';
import {
  isDeviceFlowSession,
  resolveFlowView,
  splitAuthUrl,
  type ProviderFlowState,
} from '../flowView';
import { resolveThemedIcon, type OAuthProviderCard } from '../providers';
import styles from '../OAuthPage.module.scss';

export interface OAuthFlowDialogProps {
  card: OAuthProviderCard | null;
  state: ProviderFlowState;
  theme: 'light' | 'dark';
  connected: boolean;
  isReauthTarget: boolean;
  providerText: (provider: string, suffix: string) => string;
  onStart: () => void;
  onCancel: () => void;
  onResumePolling: () => void;
  onCallbackChange: (value: string) => void;
  onSubmitCallback: () => void;
  onNavigateAccounts: () => void;
  onClose: () => void;
}

function DeviceCodeExpiryBadge({ expiresAtMs }: { expiresAtMs: number }) {
  const { t } = useTranslation();
  const [nowMs, setNowMs] = useState(() => Date.now());

  useEffect(() => {
    const timer = window.setInterval(() => setNowMs(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [expiresAtMs]);

  const remainingSec = Math.max(0, Math.ceil((expiresAtMs - nowMs) / 1000));
  const minutes = Math.floor(remainingSec / 60);
  const seconds = remainingSec % 60;
  const remainingText = `${minutes}:${String(seconds).padStart(2, '0')}`;

  return (
    <span className={styles.expiryTag}>{t('auth_login.expires_in', { time: remainingText })}</span>
  );
}

export function OAuthFlowDialog({
  card,
  state,
  theme,
  connected,
  isReauthTarget,
  providerText,
  onStart,
  onCancel,
  onResumePolling,
  onCallbackChange,
  onSubmitCallback,
  onNavigateAccounts,
  onClose,
}: OAuthFlowDialogProps) {
  const { t } = useTranslation();
  const [copiedState, setCopiedState] = useState<{ target: 'url' | 'code'; key: string } | null>(
    null
  );
  const copyTimerRef = useRef<number | null>(null);
  const callbackInputId = useId();
  const callbackErrorId = useId();

  const copyScopeKey = `${card?.id ?? ''}|${state.url ?? ''}|${state.userCode ?? ''}`;
  const copiedTarget = copiedState && copiedState.key === copyScopeKey ? copiedState.target : null;

  useEffect(() => {
    return () => {
      if (copyTimerRef.current !== null) {
        window.clearTimeout(copyTimerRef.current);
      }
    };
  }, []);

  const handleCopy = useCallback(
    async (text: string, target: 'url' | 'code') => {
      const ok = await copyToClipboard(text);
      if (!ok) return;
      setCopiedState({ target, key: copyScopeKey });
      if (copyTimerRef.current !== null) {
        window.clearTimeout(copyTimerRef.current);
      }
      copyTimerRef.current = window.setTimeout(() => {
        setCopiedState(null);
        copyTimerRef.current = null;
      }, 1800);
    },
    [copyScopeKey]
  );
  if (!card) {
    return <Modal open={false} onClose={onClose} title="" width={520}>{null}</Modal>;
  }

  const view = resolveFlowView(state);
  const isDeviceFlow = isDeviceFlowSession(state, card.flow);
  const showDeviceCodeStep = isDeviceFlow && Boolean(state.userCode);
  const showCallbackStep = !isDeviceFlow && card.supportsCallback && Boolean(state.url);
  const isDevinActive = card.id === 'devin' && Boolean(state.state) && view === 'flow';
  const callbackStepNumber = showDeviceCodeStep ? 3 : 2;

  const iconSrc = card.kind === 'builtin' ? resolveThemedIcon(card.icon, theme) : card.icon;
  const title =
    card.kind === 'builtin'
      ? t(card.titleKey)
      : t('auth_login.plugin_oauth_title', { plugin: card.title, name: card.title });
  const hint =
    card.kind === 'builtin'
      ? t(card.hintKey)
      : t('auth_login.plugin_oauth_hint', { plugin: card.title, name: card.title });
  const flowBadgeText = isDeviceFlow
    ? t('auth_login.flow_mode_device')
    : card.kind === 'plugin'
      ? t('auth_login.flow_mode_plugin')
      : t('auth_login.flow_mode_browser');

  const { host, rest } = state.url ? splitAuthUrl(state.url) : { host: '', rest: '' };
  const callbackErrorText =
    state.callbackValidation || (state.callbackStatus === 'error' ? state.callbackError : undefined);
  const isWaitingForStart = view === 'flow' && !state.url && !state.userCode;

  const modalTitle = (
    <div className={styles.dialogHeader}>
      <span className={styles.dialogIconWrap} aria-hidden="true">
        {iconSrc ? (
          <img src={iconSrc} alt="" className={styles.dialogIcon} />
        ) : (
          <IconKey size={18} className={styles.tileFallbackIcon} />
        )}
      </span>
      <div className={styles.dialogHeaderText}>
        <span className={styles.dialogTitle}>{title}</span>
        <span className={styles.dialogSubtitle}>
          {flowBadgeText}
          {view === 'flow' && !isWaitingForStart ? ` · ${t('auth_login.background_hint')}` : ''}
        </span>
      </div>
    </div>
  );

  const modalFooter =
    view === 'flow' ? (
      <div className={styles.dialogFooter}>
        <div className={styles.dialogFooterStatus}>
          {state.cancelling ? (
            <>
              <LoadingSpinner size={14} />
              <span>{t('auth_login.oauth_cancelling')}</span>
            </>
          ) : state.pollingError ? (
            <>
              <span className={styles.statusDotError} aria-hidden="true" />
              <span className={styles.statusTextError}>{t('auth_login.polling_paused')}</span>
            </>
          ) : (
            <>
              <span className={styles.statusDotWaiting} aria-hidden="true" />
              <span>{providerText(card.id, 'oauth_status_waiting')}</span>
            </>
          )}
        </div>
        <div className={styles.dialogFooterActions}>
          {state.state && (
            <Button
              variant="ghost"
              size="sm"
              className={styles.cancelButton}
              onClick={onCancel}
              loading={Boolean(state.cancelling)}
              disabled={Boolean(state.cancelling)}
            >
              {t('auth_login.cancel_auth')}
            </Button>
          )}
          <Button variant="secondary" size="sm" onClick={onClose}>
            {t('auth_login.keep_in_background')}
          </Button>
        </div>
      </div>
    ) : undefined;

  return (
    <Modal open onClose={onClose} title={modalTitle} footer={modalFooter} width={540}>
      <div className={styles.dialogBody}>
        {view === 'idle' && (
          <div className={styles.dialogStateBox}>
            <div className={styles.dialogStateTitle}>{title}</div>
            <p className={styles.dialogStateHint}>{hint}</p>
            <div className={styles.dialogStateActions}>
              <Button variant="primary" size="sm" onClick={onStart} disabled={!connected}>
                <IconExternalLink size={14} />
                <span>{providerText(card.id, 'oauth_button')}</span>
              </Button>
              <Button variant="secondary" size="sm" onClick={onClose}>
                {t('common.close')}
              </Button>
            </div>
          </div>
        )}

        {view === 'flow' && isWaitingForStart && (
          <div className={styles.dialogStateBox}>
            <LoadingSpinner size={24} />
            <div className={styles.dialogStateTitle}>{t('auth_login.starting_auth')}</div>
            <p className={styles.dialogStateHint}>{hint}</p>
          </div>
        )}

        {view === 'flow' && !isWaitingForStart && (
          <div className={styles.flowSteps}>
            <p className={styles.flowHint}>{hint}</p>

            {/* Step 1: Open Authorization URL */}
            {state.url && (
              <div className={styles.stepCard}>
                <div className={styles.stepHeader}>
                  <span className={styles.stepBadge}>1</span>
                  <span className={styles.stepTitle}>{t('auth_login.step_open_auth_page')}</span>
                  {state.expiresAtMs && state.status === 'waiting' && (
                    <DeviceCodeExpiryBadge key={state.expiresAtMs} expiresAtMs={state.expiresAtMs} />
                  )}
                </div>

                <div className={styles.urlBar} title={state.url}>
                  <span className={styles.urlText}>
                    {host && <span className={styles.urlHost}>{host}</span>}
                    <span className={styles.urlPath}>{rest}</span>
                  </span>
                  <Button
                    variant="ghost"
                    size="sm"
                    className={styles.inlineCopyButton}
                    onClick={() => void handleCopy(state.url!, 'url')}
                  >
                    {copiedTarget === 'url' ? (
                      <>
                        <IconCheck size={13} />
                        <span>{t('common.copied')}</span>
                      </>
                    ) : (
                      <>
                        <IconCopy size={13} />
                        <span>{t('auth_login.oauth_copy_link')}</span>
                      </>
                    )}
                  </Button>
                </div>

                <div className={styles.stepActions}>
                  <Button
                    variant="primary"
                    size="sm"
                    onClick={() => window.open(state.url, '_blank', 'noopener,noreferrer')}
                  >
                    <IconExternalLink size={14} />
                    <span>{t('auth_login.oauth_open_link')}</span>
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={onStart}
                    disabled={!connected || Boolean(state.cancelling) || isDevinActive}
                    title={isDevinActive ? t('auth_login.devin_cancel_before_restart') : undefined}
                  >
                    <IconRefreshCw size={14} />
                    <span>{t('auth_login.regenerate_link')}</span>
                  </Button>
                </div>
              </div>
            )}

            {/* Step 2 (Conditional): Device Code */}
            {showDeviceCodeStep && state.userCode && (
              <div className={styles.stepCard}>
                <div className={styles.stepHeader}>
                  <span className={styles.stepBadge}>2</span>
                  <span className={styles.stepTitle}>{t('auth_login.step_enter_device_code')}</span>
                </div>
                <div className={styles.deviceCodeRow}>
                  <code className={styles.deviceCodeValue}>{state.userCode}</code>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => void handleCopy(state.userCode!, 'code')}
                  >
                    {copiedTarget === 'code' ? (
                      <>
                        <IconCheck size={14} />
                        <span>{t('common.copied')}</span>
                      </>
                    ) : (
                      <>
                        <IconCopy size={14} />
                        <span>{t('auth_login.copy_device_code')}</span>
                      </>
                    )}
                  </Button>
                </div>
              </div>
            )}

            {/* Step 2/3 (Conditional): Manual Callback for remote deployments */}
            {showCallbackStep && (
              <div className={styles.stepCard}>
                <div className={styles.stepHeader}>
                  <span className={styles.stepBadgeMuted}>{callbackStepNumber}</span>
                  <span className={styles.stepTitle}>{t('auth_login.oauth_callback_label')}</span>
                  <span className={styles.optionalTag}>{t('auth_login.optional_remote_step')}</span>
                </div>
                <p className={styles.stepDescription}>
                  {card.id === 'xai'
                    ? t('auth_login.xai_callback_hint')
                    : t('auth_login.oauth_callback_hint')}
                </p>
                <form
                  className={styles.callbackForm}
                  onSubmit={(event) => {
                    event.preventDefault();
                    onSubmitCallback();
                  }}
                >
                  <div className={styles.callbackInputWrap}>
                    <Input
                      id={callbackInputId}
                      value={state.callbackUrl || ''}
                      onChange={(event) => onCallbackChange(event.target.value)}
                      placeholder={
                        card.id === 'xai'
                          ? t('auth_login.xai_callback_placeholder')
                          : t('auth_login.oauth_callback_placeholder')
                      }
                      aria-invalid={Boolean(callbackErrorText)}
                      aria-describedby={callbackErrorText ? callbackErrorId : undefined}
                      disabled={Boolean(state.callbackSubmitting) || Boolean(state.cancelling)}
                    />
                  </div>
                  <Button
                    type="submit"
                    variant="secondary"
                    size="sm"
                    className={styles.callbackSubmitBtn}
                    loading={Boolean(state.callbackSubmitting)}
                    disabled={Boolean(state.callbackSubmitting) || Boolean(state.cancelling)}
                  >
                    {t('auth_login.oauth_callback_button')}
                  </Button>
                </form>

                {callbackErrorText && (
                  <div id={callbackErrorId} role="alert" className={styles.inlineAlertError}>
                    <IconInfo size={14} />
                    <span>
                      {state.callbackValidation
                        ? state.callbackValidation
                        : `${t('auth_login.oauth_callback_status_error')} ${callbackErrorText}`}
                    </span>
                  </div>
                )}

                {state.callbackStatus === 'success' && !callbackErrorText && (
                  <div className={styles.inlineAlertSuccess}>
                    <IconCheck size={14} />
                    <span>{t('auth_login.oauth_callback_status_success')}</span>
                  </div>
                )}
              </div>
            )}

            {/* Polling network hiccup notice (non-destructive, preserves live session) */}
            {state.pollingError && (
              <div role="alert" className={styles.inlineAlertWarning}>
                <IconInfo size={14} />
                <span className={styles.inlineAlertText}>{state.pollingError}</span>
                <Button variant="secondary" size="sm" onClick={onResumePolling}>
                  {t('auth_login.retry_status_check')}
                </Button>
              </div>
            )}

            {state.cancelError && (
              <div role="alert" className={styles.inlineAlertError}>
                <IconInfo size={14} />
                <span>{state.cancelError}</span>
              </div>
            )}
          </div>
        )}

        {view === 'success' && (
          <div className={styles.dialogStateBox}>
            <span className={styles.successCircle} aria-hidden="true">
              <IconCheck size={22} />
            </span>
            <div className={styles.dialogStateTitle}>
              {providerText(card.id, 'oauth_status_success')}
            </div>
            <p className={styles.dialogStateHint}>
              {state.reauthCompleted || isReauthTarget
                ? t('auth_login.reauth_completed_hint')
                : t('auth_login.oauth_completed_hint')}
            </p>
            <div className={styles.dialogStateActions}>
              <Button variant="primary" size="sm" onClick={onNavigateAccounts}>
                {state.reauthCompleted || isReauthTarget
                  ? t('auth_login.return_to_accounts')
                  : t('auth_login.view_credentials')}
              </Button>
              <Button variant="secondary" size="sm" onClick={onStart} disabled={!connected}>
                {t('auth_login.login_another_account')}
              </Button>
            </div>
          </div>
        )}

        {view === 'failed' && (
          <div className={styles.dialogStateBox}>
            <span className={styles.errorCircle} aria-hidden="true">
              <IconInfo size={22} />
            </span>
            <div className={styles.dialogStateTitle}>
              {providerText(card.id, 'oauth_status_error')}
            </div>
            {state.error && <p className={styles.dialogErrorDetail}>{state.error}</p>}
            <div className={styles.dialogStateActions}>
              <Button variant="primary" size="sm" onClick={onStart} disabled={!connected}>
                <IconRefreshCw size={14} />
                <span>{t('auth_login.retry_login')}</span>
              </Button>
              <Button variant="secondary" size="sm" onClick={onClose}>
                {t('common.close')}
              </Button>
            </div>
          </div>
        )}
      </div>
    </Modal>
  );
}
