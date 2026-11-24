import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from 'react';
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
  IconRefreshCw,
} from '@/components/ui/icons';
import iconCodex from '@/assets/icons/codex.svg';
import { authFilesApi } from '@/services/api/authFiles';
import {
  captureSiwcBindingApi,
  type SiwcBindStartResponse,
} from '@/services/api/oauth';
import {
  publishAccountCredentialMutationRevision,
  useAuthStore,
  useNotificationStore,
} from '@/stores';
import type { AuthFileItem } from '@/types';
import { copyToClipboard } from '@/utils/clipboard';
import { createConnectionFingerprint } from '@/utils/connectionFingerprint';
import { formatDateTime } from '@/utils/format';
import { recordAccountCredentialMutationMarker } from '@/features/accounts/model/accountCredentialMutationMarker';
import {
  createSiwcStart,
  getSiwcBindingStatus,
  isSiwcCallbackValid,
  listSiwcTargets,
  readSiwcExpiresAtForDisplay,
  type SiwcStartHandle,
} from '@/entities/accounts/siwcBinding';
import { SiwcStatusBadge } from '@/components/ui/SiwcStatusBadge';
import { splitAuthUrl } from '../flowView';
import oauthStyles from '../OAuthPage.module.scss';
import styles from './SiwcBindDialog.module.scss';

type SiwcPhase = 'select' | 'starting' | 'waiting' | 'ok' | 'error' | 'expired' | 'unsupported';
type CallbackFeedback = '' | 'submitted' | 'invalid_callback' | 'callback_failed';
const POLL_INTERVAL_MS = 3000;
const DEFAULT_EXPIRES_IN_SEC = 300;
const CALLBACK_PLACEHOLDER = 'http://127.0.0.1:1455/auth/callback?code=...&state=...';

export interface SiwcBindDialogProps {
  open: boolean;
  onClose: () => void;
  onSuccess?: () => void;
  onViewAccounts: () => void;
}

function captureConnectionSnapshot() {
  const auth = useAuthStore.getState();
  const requestScope = { apiBase: auth.apiBase, managementKey: auth.managementKey };
  return {
    isAuthenticated: auth.isAuthenticated,
    apiBase: auth.apiBase,
    managementKey: auth.managementKey,
    requestScope,
    connectionFingerprint: createConnectionFingerprint(auth.apiBase, auth.managementKey),
    siwcApi: captureSiwcBindingApi(requestScope),
  };
}

function getErrorStatus(error: unknown): number | undefined {
  if (typeof error !== 'object' || error === null) return undefined;
  if ('status' in error && typeof error.status === 'number') {
    return error.status;
  }
  if (
    'response' in error &&
    typeof error.response === 'object' &&
    error.response !== null &&
    'status' in error.response &&
    typeof error.response.status === 'number'
  ) {
    return error.response.status;
  }
  return undefined;
}

function formatExpiryDate(file: AuthFileItem, locale?: string): string {
  const raw = readSiwcExpiresAtForDisplay(file);
  if (!raw) return '';
  const formatted = formatDateTime(raw, locale);
  return formatted === 'Invalid Date' ? raw : formatted;
}

export function SiwcBindDialog({
  open,
  onClose,
  onSuccess,
  onViewAccounts,
}: SiwcBindDialogProps) {
  const { t, i18n } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);

  const [prevOpen, setPrevOpen] = useState(open);
  const [capturedConnection, setCapturedConnection] = useState(captureConnectionSnapshot);
  const [targets, setTargets] = useState<AuthFileItem[] | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [selectedName, setSelectedName] = useState('');
  const [phase, setPhase] = useState<SiwcPhase>('select');
  const [session, setSession] = useState<SiwcBindStartResponse | null>(null);
  const [callbackUrl, setCallbackUrl] = useState('');
  const [feedback, setFeedback] = useState<CallbackFeedback>('');
  const [pollRetrying, setPollRetrying] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [copiedUrl, setCopiedUrl] = useState<string | null>(null);

  if (open !== prevOpen) {
    setPrevOpen(open);
    if (open) {
      setCapturedConnection(captureConnectionSnapshot());
      setTargets(null);
      setLoadFailed(false);
      setSelectedName('');
      setPhase('select');
      setSession(null);
      setCallbackUrl('');
      setFeedback('');
      setPollRetrying(false);
      setSubmitting(false);
      setCopiedUrl(null);
    }
  }

  const radioGroupName = useId();
  const callbackInputId = useId();
  const callbackHintId = useId();
  const callbackFeedbackId = useId();

  const closedRef = useRef(false);
  const runIdRef = useRef(0);
  const submittingRef = useRef(false);
  const activeTargetNameRef = useRef('');
  const pollTimerRef = useRef<number | null>(null);
  const expiryTimerRef = useRef<number | null>(null);
  const copyTimerRef = useRef<number | null>(null);
  const abortControllerRef = useRef<AbortController | null>(null);
  const startHandleRef = useRef<SiwcStartHandle | null>(null);
  const activeSessionStateRef = useRef<string | null>(null);
  const onCloseRef = useRef(onClose);
  const onSuccessRef = useRef(onSuccess);
  const tRef = useRef(t);
  const showNotificationRef = useRef(showNotification);

  useLayoutEffect(() => {
    onCloseRef.current = onClose;
    onSuccessRef.current = onSuccess;
    tRef.current = t;
    showNotificationRef.current = showNotification;
  }, [onClose, onSuccess, showNotification, t]);

  const isSameConnection = useCallback(() => {
    const current = useAuthStore.getState();
    return (
      current.isAuthenticated === capturedConnection.isAuthenticated &&
      current.apiBase === capturedConnection.apiBase &&
      current.managementKey === capturedConnection.managementKey
    );
  }, [capturedConnection]);

  const clearTimersAndAbort = useCallback(() => {
    if (pollTimerRef.current !== null) {
      window.clearTimeout(pollTimerRef.current);
      pollTimerRef.current = null;
    }
    if (expiryTimerRef.current !== null) {
      window.clearTimeout(expiryTimerRef.current);
      expiryTimerRef.current = null;
    }
    abortControllerRef.current?.abort();
    abortControllerRef.current = null;
  }, []);

  const cancelActiveSession = useCallback(() => {
    const activeState = activeSessionStateRef.current;
    activeSessionStateRef.current = null;
    if (activeState) {
      void capturedConnection.siwcApi.cancelSession(activeState).catch(() => {});
    }
  }, [capturedConnection]);

  const isRunCurrent = useCallback(
    (runId: number) => !closedRef.current && runIdRef.current === runId && isSameConnection(),
    [isSameConnection]
  );

  const abortCurrentRun = useCallback(() => {
    runIdRef.current += 1;
    submittingRef.current = false;
    clearTimersAndAbort();
    startHandleRef.current?.cancel();
    startHandleRef.current = null;
    cancelActiveSession();
  }, [cancelActiveSession, clearTimersAndAbort]);

  useEffect(() => {
    if (!open) return;
    closedRef.current = false;
    submittingRef.current = false;
    activeTargetNameRef.current = '';
    let active = true;

    const unsubscribe =
      typeof useAuthStore.subscribe === 'function'
        ? useAuthStore.subscribe(() => {
            if (!isSameConnection()) {
              active = false;
              closedRef.current = true;
              abortCurrentRun();
              onCloseRef.current();
            }
          })
        : undefined;

    authFilesApi
      .list(capturedConnection.requestScope)
      .then((response) => {
        if (!active || closedRef.current || !isSameConnection()) return;
        const list = listSiwcTargets(response.files ?? []);
        setTargets(list);
        if (list.length === 1) {
          setSelectedName(list[0].name);
        }
      })
      .catch(() => {
        if (!active || closedRef.current || !isSameConnection()) return;
        setLoadFailed(true);
      });

    return () => {
      active = false;
      closedRef.current = true;
      unsubscribe?.();
      abortCurrentRun();
      if (copyTimerRef.current !== null) {
        window.clearTimeout(copyTimerRef.current);
        copyTimerRef.current = null;
      }
    };
  }, [abortCurrentRun, capturedConnection.requestScope, isSameConnection, open]);

  const handleClose = useCallback(() => {
    closedRef.current = true;
    abortCurrentRun();
    onClose();
  }, [abortCurrentRun, onClose]);

  const handleRestart = useCallback(() => {
    abortCurrentRun();
    activeTargetNameRef.current = '';
    setSession(null);
    setCallbackUrl('');
    setFeedback('');
    setPollRetrying(false);
    setSubmitting(false);
    setCopiedUrl(null);
    setPhase('select');
  }, [abortCurrentRun]);

  const handleViewAccounts = useCallback(() => {
    closedRef.current = true;
    abortCurrentRun();
    onViewAccounts();
  }, [abortCurrentRun, onViewAccounts]);

  const handleCopyUrl = useCallback(async (url: string) => {
    const ok = await copyToClipboard(url);
    if (!ok) return;
    setCopiedUrl(url);
    if (copyTimerRef.current !== null) {
      window.clearTimeout(copyTimerRef.current);
    }
    copyTimerRef.current = window.setTimeout(() => {
      setCopiedUrl(null);
      copyTimerRef.current = null;
    }, 1800);
  }, []);

  const pollOnce = useCallback(
    async function poll(
      state: string,
      targetName: string,
      runId: number
    ): Promise<'ok' | 'wait' | 'error' | 'expired' | 'transient' | 'stale'> {
      if (pollTimerRef.current !== null) {
        window.clearTimeout(pollTimerRef.current);
        pollTimerRef.current = null;
      }
      if (!isRunCurrent(runId)) return 'stale';

      const scheduleNext = () => {
        if (pollTimerRef.current !== null) {
          window.clearTimeout(pollTimerRef.current);
        }
        pollTimerRef.current = window.setTimeout(() => {
          pollTimerRef.current = null;
          void poll(state, targetName, runId);
        }, POLL_INTERVAL_MS);
      };

      const signal = abortControllerRef.current?.signal;
      try {
        const statusResponse = await capturedConnection.siwcApi.getStatus(state, signal);
        if (!isRunCurrent(runId)) return 'stale';

        setPollRetrying(false);

        if (statusResponse.status === 'ok') {
          runIdRef.current += 1;
          activeSessionStateRef.current = null;
          submittingRef.current = false;
          clearTimersAndAbort();
          setSubmitting(false);
          setPhase('ok');
          if (capturedConnection.connectionFingerprint) {
            recordAccountCredentialMutationMarker({
              connectionFingerprint: capturedConnection.connectionFingerprint,
              provider: 'codex',
            });
            publishAccountCredentialMutationRevision({
              connectionFingerprint: capturedConnection.connectionFingerprint,
              provider: 'codex',
              kind: 'oauth',
              credentialIdentity: targetName,
            });
          }
          showNotificationRef.current(tRef.current('siwc.ok'), 'success');
          onSuccessRef.current?.();
          return 'ok';
        }

        if (statusResponse.status === 'error') {
          runIdRef.current += 1;
          activeSessionStateRef.current = null;
          submittingRef.current = false;
          clearTimersAndAbort();
          setSubmitting(false);
          setPhase('error');
          return 'error';
        }

        scheduleNext();
        return 'wait';
      } catch (error: unknown) {
        if (!isRunCurrent(runId)) return 'stale';
        if (getErrorStatus(error) === 404) {
          runIdRef.current += 1;
          activeSessionStateRef.current = null;
          submittingRef.current = false;
          clearTimersAndAbort();
          setSubmitting(false);
          setPollRetrying(false);
          setPhase('expired');
          return 'expired';
        }

        // Transient polling failure: preserve the active server session, keep waiting, and retry.
        setPollRetrying(true);
        scheduleNext();
        return 'transient';
      }
    },
    [capturedConnection, clearTimersAndAbort, isRunCurrent]
  );

  const handleRetryPoll = useCallback(() => {
    if (!session || phase !== 'waiting') return;
    const targetName = activeTargetNameRef.current || selectedName;
    void pollOnce(session.state, targetName, runIdRef.current);
  }, [phase, pollOnce, selectedName, session]);

  const handleStart = useCallback(() => {
    if (!selectedName || phase !== 'select') return;
    if (!isSameConnection()) {
      handleClose();
      return;
    }

    abortCurrentRun();
    const runId = runIdRef.current;
    const targetName = selectedName;
    activeTargetNameRef.current = targetName;
    const controller = new AbortController();
    abortControllerRef.current = controller;

    setSession(null);
    setCallbackUrl('');
    setFeedback('');
    setPollRetrying(false);
    setSubmitting(false);
    setCopiedUrl(null);
    setPhase('starting');

    const startHandle = createSiwcStart(targetName, {
      start: (name) => capturedConnection.siwcApi.start(name),
      cancelSession: (state) => capturedConnection.siwcApi.cancelSession(state),
      isClosed: () => !isRunCurrent(runId),
    });
    startHandleRef.current = startHandle;

    void startHandle.promise.then(
      (response) => {
        if (!startHandle.isCurrent() || !isRunCurrent(runId)) return;
        startHandleRef.current = null;

        const state = typeof response?.state === 'string' ? response.state.trim() : '';
        const rawUrl = typeof response?.url === 'string' ? response.url.trim() : '';
        let parsedUrl: URL | null = null;
        try {
          parsedUrl = rawUrl ? new URL(rawUrl) : null;
        } catch {
          parsedUrl = null;
        }
        const statusValid = response?.status === undefined || response.status === 'ok';
        const flowValid = response?.flow === undefined || response.flow === 'siwc_bind';
        const protocolValid =
          parsedUrl !== null && (parsedUrl.protocol === 'https:' || parsedUrl.protocol === 'http:');

        if (!state || !protocolValid || !statusValid || !flowValid) {
          runIdRef.current += 1;
          if (state) {
            void capturedConnection.siwcApi.cancelSession(state).catch(() => {});
          }
          clearTimersAndAbort();
          setPhase('error');
          return;
        }

        const expiresInSec =
          typeof response.expires_in === 'number' &&
          Number.isFinite(response.expires_in) &&
          response.expires_in > 0
            ? response.expires_in
            : DEFAULT_EXPIRES_IN_SEC;

        activeSessionStateRef.current = state;
        setSession({
          ...response,
          state,
          url: rawUrl,
          expires_in: expiresInSec,
        });
        setPhase('waiting');

        expiryTimerRef.current = window.setTimeout(() => {
          expiryTimerRef.current = null;
          if (!isRunCurrent(runId)) return;
          runIdRef.current += 1;
          submittingRef.current = false;
          clearTimersAndAbort();
          cancelActiveSession();
          setSubmitting(false);
          setPollRetrying(false);
          setPhase('expired');
        }, expiresInSec * 1000);

        pollTimerRef.current = window.setTimeout(() => {
          pollTimerRef.current = null;
          void pollOnce(state, targetName, runId);
        }, POLL_INTERVAL_MS);
      },
      (error: unknown) => {
        if (!startHandle.isCurrent() || !isRunCurrent(runId)) return;
        startHandleRef.current = null;
        runIdRef.current += 1;
        clearTimersAndAbort();
        setPhase(getErrorStatus(error) === 404 ? 'unsupported' : 'error');
      }
    );
  }, [
    abortCurrentRun,
    cancelActiveSession,
    capturedConnection,
    clearTimersAndAbort,
    handleClose,
    isRunCurrent,
    isSameConnection,
    phase,
    pollOnce,
    selectedName,
  ]);

  const handleSubmitCallback = useCallback(async () => {
    if (!session || phase !== 'waiting' || submittingRef.current) return;
    const trimmed = callbackUrl.trim();
    if (!isSiwcCallbackValid(trimmed, session.state)) {
      setFeedback('invalid_callback');
      return;
    }

    const runId = runIdRef.current;
    const signal = abortControllerRef.current?.signal;
    const targetName = activeTargetNameRef.current || selectedName;

    submittingRef.current = true;
    setSubmitting(true);
    setFeedback('');
    try {
      await capturedConnection.siwcApi.submitCallback('codex', trimmed, signal);
      if (!isRunCurrent(runId)) return;
      setFeedback('submitted');
      await pollOnce(session.state, targetName, runId);
    } catch (error: unknown) {
      if (!isRunCurrent(runId)) return;
      if (getErrorStatus(error) === 409) {
        setFeedback('submitted');
        await pollOnce(session.state, targetName, runId);
        return;
      }
      setFeedback('callback_failed');
    } finally {
      if (isRunCurrent(runId)) {
        submittingRef.current = false;
        setSubmitting(false);
      }
    }
  }, [callbackUrl, capturedConnection, isRunCurrent, phase, pollOnce, selectedName, session]);

  const selectedTarget = targets?.find((file) => file.name === selectedName) ?? null;
  const urlParts = session?.url ? splitAuthUrl(session.url) : null;
  const isCopied = Boolean(session?.url && copiedUrl === session.url);
  const isCallbackError = feedback === 'invalid_callback' || feedback === 'callback_failed';
  const locale = i18n?.language;

  const modalTitle = (
    <div className={oauthStyles.dialogHeader}>
      <span className={oauthStyles.dialogIconWrap} aria-hidden="true">
        <img src={iconCodex} alt="" className={oauthStyles.dialogIcon} />
      </span>
      <div className={oauthStyles.dialogHeaderText}>
        <span className={oauthStyles.dialogTitle}>{t('siwc.title')}</span>
        <span className={oauthStyles.dialogSubtitle}>
          {`Codex · ${t('auth_login.flow_mode_browser')}`}
        </span>
      </div>
    </div>
  );

  const modalFooter =
    phase === 'select' ? (
      <div className={oauthStyles.dialogFooterEnd}>
        <Button variant="secondary" size="sm" onClick={handleClose}>
          {t('common.close')}
        </Button>
        <Button
          variant="primary"
          size="sm"
          disabled={!selectedName || targets === null || loadFailed}
          onClick={handleStart}
        >
          {t('siwc.start')}
        </Button>
      </div>
    ) : phase === 'starting' || phase === 'waiting' ? (
      <div className={oauthStyles.dialogFooter}>
        <div className={oauthStyles.dialogFooterStatus} role="status" aria-live="polite">
          {phase === 'starting' ? (
            <LoadingSpinner size={14} />
          ) : (
            <span className={oauthStyles.statusDotWaiting} aria-hidden="true" />
          )}
          <span>{t(phase === 'starting' ? 'siwc.starting' : 'siwc.wait')}</span>
        </div>
        <div className={oauthStyles.dialogFooterActions}>
          <Button
            variant="ghost"
            size="sm"
            className={oauthStyles.cancelButton}
            onClick={handleClose}
          >
            {t('siwc.cancel')}
          </Button>
        </div>
      </div>
    ) : undefined;

  const renderTargetSummary = () => {
    if (!selectedTarget) return null;
    return (
      <div className={styles.summary}>
        <span className={styles.summaryLabel}>{t('siwc.target')}</span>
        <span className={styles.summaryName} title={selectedTarget.name}>
          {selectedTarget.name}
        </span>
        <SiwcStatusBadge status={getSiwcBindingStatus(selectedTarget)} />
      </div>
    );
  };

  return (
    <Modal open={open} onClose={handleClose} title={modalTitle} footer={modalFooter} width={540}>
      <div className={oauthStyles.dialogBody}>
        {phase === 'select' && (
          <div className={oauthStyles.flowSteps}>
            <p className={oauthStyles.flowHint}>{t('siwc.intro')}</p>

            <fieldset className={styles.picker}>
              <legend className={styles.legend}>{t('siwc.target')}</legend>

              {loadFailed ? (
                <div role="alert" className={oauthStyles.inlineAlertError}>
                  <IconInfo size={14} />
                  <span>{t('siwc.load_failed')}</span>
                </div>
              ) : targets === null ? (
                <div
                  className={styles.options}
                  aria-busy="true"
                  aria-label={t('siwc.loading')}
                >
                  <span className={styles.skeletonRow} />
                  <span className={styles.skeletonRow} />
                </div>
              ) : targets.length === 0 ? (
                <p className={styles.empty}>{t('siwc.no_targets')}</p>
              ) : (
                <div className={styles.options}>
                  {targets.map((file) => {
                    const status = getSiwcBindingStatus(file);
                    const expiresText = formatExpiryDate(file, locale);
                    const checked = selectedName === file.name;
                    return (
                      <label
                        key={file.name}
                        className={styles.option}
                        data-checked={checked || undefined}
                      >
                        <input
                          type="radio"
                          className={styles.radioInput}
                          name={radioGroupName}
                          value={file.name}
                          checked={checked}
                          onChange={() => setSelectedName(file.name)}
                        />
                        <span className={styles.radio} aria-hidden="true" />
                        <span className={styles.optionText}>
                          <span className={styles.optionName} title={file.name}>
                            {file.name}
                          </span>
                          {expiresText && (
                            <span className={styles.optionMeta}>
                              {t('siwc.expires', { date: expiresText })}
                            </span>
                          )}
                        </span>
                        <SiwcStatusBadge status={status} />
                      </label>
                    );
                  })}
                </div>
              )}
            </fieldset>
          </div>
        )}

        {(phase === 'starting' || phase === 'waiting') && (
          <div className={oauthStyles.flowSteps}>
            {renderTargetSummary()}

            <div className={oauthStyles.stepCard}>
              <div className={oauthStyles.stepHeader}>
                <span className={oauthStyles.stepBadge}>1</span>
                <span className={oauthStyles.stepTitle}>{t('siwc.step_open')}</span>
              </div>

              <div
                className={oauthStyles.urlBar}
                title={session?.url}
                aria-busy={phase === 'starting'}
              >
                {urlParts && session?.url ? (
                  <span className={oauthStyles.urlText}>
                    {urlParts.host && (
                      <span className={oauthStyles.urlHost}>{urlParts.host}</span>
                    )}
                    <span className={oauthStyles.urlPath}>{urlParts.rest}</span>
                  </span>
                ) : (
                  <span className={styles.urlSkeleton} aria-label={t('siwc.starting')} />
                )}
                {session?.url && (
                  <Button
                    variant="ghost"
                    size="sm"
                    className={oauthStyles.inlineCopyButton}
                    onClick={() => void handleCopyUrl(session.url)}
                  >
                    {isCopied ? (
                      <>
                        <IconCheck size={13} />
                        <span>{t('common.copied')}</span>
                      </>
                    ) : (
                      <>
                        <IconCopy size={13} />
                        <span>{t('siwc.copy')}</span>
                      </>
                    )}
                  </Button>
                )}
              </div>

              <div className={oauthStyles.stepActions}>
                <Button
                  variant="primary"
                  size="sm"
                  disabled={!session?.url}
                  onClick={() => {
                    if (session?.url) {
                      window.open(session.url, '_blank', 'noopener,noreferrer');
                    }
                  }}
                >
                  <IconExternalLink size={14} />
                  <span>{t('siwc.open')}</span>
                </Button>
              </div>
            </div>

            <div className={oauthStyles.stepCard}>
              <div className={oauthStyles.stepHeader}>
                <span className={oauthStyles.stepBadgeMuted}>2</span>
                <label htmlFor={callbackInputId} className={oauthStyles.stepTitle}>
                  {t('siwc.callback')}
                </label>
              </div>
              <p id={callbackHintId} className={oauthStyles.stepDescription}>
                {t('siwc.hint')}
              </p>

              <form
                className={oauthStyles.callbackForm}
                onSubmit={(event) => {
                  event.preventDefault();
                  void handleSubmitCallback();
                }}
              >
                <div className={oauthStyles.callbackInputWrap}>
                  <Input
                    id={callbackInputId}
                    value={callbackUrl}
                    onChange={(event) => {
                      setCallbackUrl(event.target.value);
                      if (feedback && feedback !== 'submitted') {
                        setFeedback('');
                      }
                    }}
                    placeholder={CALLBACK_PLACEHOLDER}
                    autoComplete="off"
                    spellCheck={false}
                    aria-invalid={isCallbackError}
                    aria-describedby={
                      feedback ? `${callbackFeedbackId} ${callbackHintId}` : callbackHintId
                    }
                    disabled={phase !== 'waiting' || submitting}
                  />
                </div>
                <Button
                  type="submit"
                  variant="secondary"
                  size="sm"
                  className={oauthStyles.callbackSubmitBtn}
                  loading={submitting}
                  disabled={phase !== 'waiting' || submitting || !callbackUrl.trim()}
                >
                  {t('siwc.submit')}
                </Button>
              </form>

              {feedback && (
                <div
                  id={callbackFeedbackId}
                  role={isCallbackError ? 'alert' : 'status'}
                  className={
                    isCallbackError
                      ? oauthStyles.inlineAlertError
                      : oauthStyles.inlineAlertSuccess
                  }
                >
                  {isCallbackError ? <IconInfo size={14} /> : <IconCheck size={14} />}
                  <span>{t(`siwc.${feedback}`)}</span>
                </div>
              )}
            </div>

            {phase === 'waiting' && pollRetrying && (
              <div role="alert" className={oauthStyles.inlineAlertWarning}>
                <IconInfo size={14} />
                <span className={oauthStyles.inlineAlertText}>{t('siwc.poll_retrying')}</span>
                <Button variant="secondary" size="sm" onClick={handleRetryPoll}>
                  {t('siwc.retry_poll')}
                </Button>
              </div>
            )}
          </div>
        )}

        {(phase === 'error' || phase === 'expired' || phase === 'unsupported') && (
          <div className={oauthStyles.flowSteps}>
            {renderTargetSummary()}
            <div className={oauthStyles.dialogStateBox}>
              <span
                className={
                  phase === 'error' ? oauthStyles.errorCircle : styles.warningCircle
                }
                aria-hidden="true"
              >
                <IconInfo size={22} />
              </span>
              <div className={oauthStyles.dialogStateTitle} role="alert">
                {t(`siwc.${phase}`)}
              </div>
              <div className={oauthStyles.dialogStateActions}>
                {phase !== 'unsupported' && (
                  <Button variant="primary" size="sm" onClick={handleRestart}>
                    <IconRefreshCw size={14} />
                    <span>{t('siwc.retry')}</span>
                  </Button>
                )}
                <Button variant="secondary" size="sm" onClick={handleClose}>
                  {t('common.close')}
                </Button>
              </div>
            </div>
          </div>
        )}

        {phase === 'ok' && (
          <div className={oauthStyles.dialogStateBox} role="status">
            <span className={oauthStyles.successCircle} aria-hidden="true">
              <IconCheck size={22} />
            </span>
            <div className={oauthStyles.dialogStateTitle}>{t('siwc.ok')}</div>
            {selectedName && (
              <p className={oauthStyles.dialogStateHint}>
                {t('siwc.ok_hint', { name: selectedName })}
              </p>
            )}
            <div className={oauthStyles.dialogStateActions}>
              <Button variant="primary" size="sm" onClick={handleViewAccounts}>
                {t('auth_login.view_credentials')}
              </Button>
              <Button variant="secondary" size="sm" onClick={handleClose}>
                {t('siwc.done')}
              </Button>
            </div>
          </div>
        )}
      </div>
    </Modal>
  );
}
