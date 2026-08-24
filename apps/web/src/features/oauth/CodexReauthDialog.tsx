import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { IconCheck, IconCopy, IconExternalLink, IconRefreshCw } from '@/components/ui/icons';
import { oauthApi } from '@/services/api';
import type { ApiClientRequestScope } from '@/services/api/client';
import { useNotificationStore } from '@/stores';
import { copyToClipboard } from '@/utils/clipboard';
import type { CodexReauthTarget } from './codexReauthModel';
import styles from './CodexReauthDialog.module.scss';

type CodexReauthStatus = 'idle' | 'loading' | 'waiting' | 'synchronizing' | 'success' | 'error';

type CodexReauthDialogProps = {
  open: boolean;
  target: CodexReauthTarget | null;
  requestScope?: ApiClientRequestScope;
  onClose: () => void;
  onSuccess?: () => void | Promise<void>;
};

const POLL_INTERVAL_MS = 3000;

type CodexReauthDialogContext = {
  open: boolean;
  targetKey: string;
  apiBase: string;
  managementKey: string;
};

const isSameDialogContext = (
  left: CodexReauthDialogContext,
  right: CodexReauthDialogContext
): boolean =>
  left.open === right.open &&
  left.targetKey === right.targetKey &&
  left.apiBase === right.apiBase &&
  left.managementKey === right.managementKey;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object';

const getErrorMessage = (error: unknown): string => {
  if (error instanceof Error) return error.message;
  if (isRecord(error) && typeof error.message === 'string') return error.message;
  return typeof error === 'string' ? error : '';
};

export function CodexReauthDialog({
  open,
  target,
  requestScope,
  onClose,
  onSuccess,
}: CodexReauthDialogProps) {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const [authUrl, setAuthUrl] = useState('');
  const [status, setStatus] = useState<CodexReauthStatus>('idle');
  const [error, setError] = useState('');
  const [callbackUrl, setCallbackUrl] = useState('');
  const [callbackSubmitting, setCallbackSubmitting] = useState(false);
  const [callbackStatus, setCallbackStatus] = useState<'success' | 'error' | undefined>();
  const [callbackError, setCallbackError] = useState('');
  const [copiedTarget, setCopiedTarget] = useState<'account' | 'link' | null>(null);
  const [linkRefreshed, setLinkRefreshed] = useState(false);
  const pollingTimerRef = useRef<number | null>(null);
  const feedbackTimerRef = useRef<number | null>(null);
  const successHandledRef = useRef(false);
  const operationGenerationRef = useRef(0);

  const targetKey = useMemo(
    () =>
      target
        ? [target.account, target.fileName ?? '', target.accountId ?? ''].join('\u0000')
        : '',
    // Keep primitive fields only. Including `target` would restart OAuth after Accounts reload.
    // eslint-disable-next-line react-hooks/exhaustive-deps -- stable session identity
    [target?.account, target?.accountId, target?.fileName]
  );
  const dialogContext = useMemo<CodexReauthDialogContext>(
    () => ({
      open,
      targetKey,
      apiBase: requestScope?.apiBase ?? '',
      managementKey: requestScope?.managementKey ?? '',
    }),
    [open, requestScope?.apiBase, requestScope?.managementKey, targetKey]
  );
  const activeDialogContextRef = useRef(dialogContext);
  useLayoutEffect(() => {
    activeDialogContextRef.current = dialogContext;
  }, [dialogContext]);

  const isCurrentOperation = useCallback(
    (operationGeneration: number, operationContext: CodexReauthDialogContext) =>
      operationGenerationRef.current === operationGeneration &&
      isSameDialogContext(activeDialogContextRef.current, operationContext),
    []
  );

  const clearPolling = useCallback(() => {
    if (pollingTimerRef.current !== null) {
      window.clearInterval(pollingTimerRef.current);
      pollingTimerRef.current = null;
    }
  }, []);

  const clearFeedbackTimer = useCallback(() => {
    if (feedbackTimerRef.current !== null) {
      window.clearTimeout(feedbackTimerRef.current);
      feedbackTimerRef.current = null;
    }
  }, []);

  const showTemporaryFeedback = useCallback(
    (callback: () => void) => {
      clearFeedbackTimer();
      callback();
      feedbackTimerRef.current = window.setTimeout(() => {
        setCopiedTarget(null);
        setLinkRefreshed(false);
        feedbackTimerRef.current = null;
      }, 1800);
    },
    [clearFeedbackTimer]
  );

  const markSuccess = useCallback(
    async (operationGeneration: number, operationContext: CodexReauthDialogContext) => {
      if (!isCurrentOperation(operationGeneration, operationContext)) return;
      if (successHandledRef.current) return;
      successHandledRef.current = true;
      operationGenerationRef.current += 1;
      const successGeneration = operationGenerationRef.current;
      clearPolling();
      setStatus('synchronizing');
      setError('');
      setCallbackSubmitting(true);
      setCallbackStatus(undefined);
      setCallbackError('');
      try {
        await onSuccess?.();
        if (
          operationGenerationRef.current !== successGeneration ||
          !isSameDialogContext(activeDialogContextRef.current, operationContext)
        ) {
          return;
        }
        setStatus('success');
        setCallbackSubmitting(false);
        setCallbackStatus('success');
        showNotification(t('codex_reauth.success'), 'success');
      } catch (err: unknown) {
        if (
          operationGenerationRef.current !== successGeneration ||
          !isSameDialogContext(activeDialogContextRef.current, operationContext)
        ) {
          return;
        }
        const message = getErrorMessage(err) || t('notification.refresh_failed');
        const warning = `${t('notification.refresh_failed')}: ${message}`;
        setStatus('success');
        setCallbackSubmitting(false);
        setCallbackStatus('success');
        setCallbackError(warning);
        showNotification(`${t('codex_reauth.success')}；${warning}`, 'warning');
      }
    },
    [clearPolling, isCurrentOperation, onSuccess, showNotification, t]
  );

  const startPolling = useCallback(
    (state: string, operationGeneration: number, operationContext: CodexReauthDialogContext) => {
      clearPolling();
      pollingTimerRef.current = window.setInterval(async () => {
        try {
          const response = await oauthApi.getAuthStatus(state, requestScope);
          if (!isCurrentOperation(operationGeneration, operationContext)) return;
          if (response.status === 'ok') {
            await markSuccess(operationGeneration, operationContext);
            return;
          }
          if (response.status === 'error') {
            operationGenerationRef.current += 1;
            clearPolling();
            const message = response.error || t('codex_reauth.error');
            setStatus('error');
            setError(message);
            showNotification(message, 'error');
          }
        } catch (err: unknown) {
          if (!isCurrentOperation(operationGeneration, operationContext)) return;
          operationGenerationRef.current += 1;
          clearPolling();
          const message = getErrorMessage(err) || t('codex_reauth.error');
          setStatus('error');
          setError(message);
        }
      }, POLL_INTERVAL_MS);
    },
    [clearPolling, isCurrentOperation, markSuccess, requestScope, showNotification, t]
  );

  const loadAuthLink = useCallback(
    async (showRefreshFeedback = false) => {
      const operationGeneration = operationGenerationRef.current + 1;
      const operationContext = activeDialogContextRef.current;
      operationGenerationRef.current = operationGeneration;
      clearPolling();
      successHandledRef.current = false;
      setAuthUrl('');
      setStatus('loading');
      setError('');
      setCallbackUrl('');
      setCallbackSubmitting(false);
      setCallbackStatus(undefined);
      setCallbackError('');
      setCopiedTarget(null);
      setLinkRefreshed(false);
      try {
        const response = await oauthApi.startAuth('codex', requestScope);
        if (!isCurrentOperation(operationGeneration, operationContext)) return;
        if (!response.state) {
          const message = t('codex_reauth.missing_state');
          setAuthUrl(response.url);
          setStatus('error');
          setError(message);
          showNotification(message, 'error');
          return;
        }
        setAuthUrl(response.url);
        setStatus('waiting');
        if (showRefreshFeedback) {
          showTemporaryFeedback(() => setLinkRefreshed(true));
        }
        startPolling(response.state, operationGeneration, operationContext);
      } catch (err: unknown) {
        if (!isCurrentOperation(operationGeneration, operationContext)) return;
        const message = getErrorMessage(err) || t('codex_reauth.error');
        setStatus('error');
        setError(message);
        showNotification(message, 'error');
      }
    },
    [
      clearPolling,
      isCurrentOperation,
      requestScope,
      showNotification,
      showTemporaryFeedback,
      startPolling,
      t,
    ]
  );
  const loadAuthLinkRef = useRef(loadAuthLink);
  useLayoutEffect(() => {
    loadAuthLinkRef.current = loadAuthLink;
  }, [loadAuthLink]);

  useEffect(() => {
    if (!open || !targetKey) {
      operationGenerationRef.current += 1;
      clearPolling();
      clearFeedbackTimer();
      return;
    }
    const timer = window.setTimeout(() => {
      void loadAuthLinkRef.current();
    }, 0);
    return () => {
      operationGenerationRef.current += 1;
      window.clearTimeout(timer);
      clearPolling();
      clearFeedbackTimer();
    };
  }, [
    clearFeedbackTimer,
    clearPolling,
    open,
    requestScope?.apiBase,
    requestScope?.managementKey,
    targetKey,
  ]);

  useEffect(
    () => () => {
      clearPolling();
      clearFeedbackTimer();
    },
    [clearFeedbackTimer, clearPolling]
  );

  const copyText = useCallback(
    async (text: string, targetName: 'account' | 'link') => {
      if (!text) return;
      const copied = await copyToClipboard(text);
      if (copied) {
        showTemporaryFeedback(() => setCopiedTarget(targetName));
      }
      showNotification(
        t(copied ? 'notification.link_copied' : 'notification.copy_failed'),
        copied ? 'success' : 'error'
      );
    },
    [showNotification, showTemporaryFeedback, t]
  );

  const openAuthUrl = useCallback(() => {
    if (!authUrl) return;
    window.open(authUrl, '_blank', 'noopener,noreferrer');
  }, [authUrl]);

  const submitCallback = useCallback(async () => {
    const redirectUrl = callbackUrl.trim();
    if (!redirectUrl) {
      showNotification(t('codex_reauth.callback_required'), 'warning');
      return;
    }
    const operationGeneration = operationGenerationRef.current;
    const operationContext = activeDialogContextRef.current;
    setCallbackSubmitting(true);
    setCallbackStatus(undefined);
    setCallbackError('');
    try {
      await oauthApi.submitCallback('codex', redirectUrl, requestScope);
      if (!isCurrentOperation(operationGeneration, operationContext)) return;
      await markSuccess(operationGeneration, operationContext);
    } catch (err: unknown) {
      if (!isCurrentOperation(operationGeneration, operationContext)) return;
      const message = getErrorMessage(err) || t('codex_reauth.error');
      setCallbackSubmitting(false);
      setCallbackStatus('error');
      setCallbackError(message);
      showNotification(`${t('codex_reauth.error')} ${message}`.trim(), 'error');
    }
  }, [callbackUrl, isCurrentOperation, markSuccess, requestScope, showNotification, t]);

  const statusNode = (() => {
    if (status === 'loading') {
      return (
        <div className={`${styles.status} ${styles.statusWaiting}`}>
          {t('codex_reauth.loading_link')}
        </div>
      );
    }
    if (status === 'waiting') {
      return (
        <div className={`${styles.status} ${styles.statusWaiting}`}>
          {t('codex_reauth.waiting')}
        </div>
      );
    }
    if (status === 'synchronizing') {
      return (
        <div className={`${styles.status} ${styles.statusWaiting}`}>
          {t('codex_reauth.synchronizing')}
        </div>
      );
    }
    if (status === 'success') {
      return (
        <div className={`${styles.status} ${styles.statusSuccess}`}>
          {t('codex_reauth.success')}
        </div>
      );
    }
    if (status === 'error') {
      return (
        <div className={`${styles.status} ${styles.statusError}`}>
          {error || t('codex_reauth.error')}
        </div>
      );
    }
    return null;
  })();

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('codex_reauth.title')}
      width={620}
      footer={
        <div className={styles.footer}>
          <Button variant="secondary" size="sm" onClick={onClose}>
            {t('common.close')}
          </Button>
        </div>
      }
    >
      <div className={styles.dialogBody}>
        <p className={styles.hint}>{t('codex_reauth.same_account_hint')}</p>

        <div className={styles.accountSummary}>
          <span className={styles.summaryLabel}>{t('codex_reauth.account_label')}</span>
          <span className={styles.summaryValue}>{target?.account || '-'}</span>
          <Button
            type="button"
            variant="ghost"
            size="xs"
            onClick={() => void copyText(target?.account || '', 'account')}
            disabled={!target?.account}
          >
            <IconCopy size={13} />
            {copiedTarget === 'account' ? t('codex_reauth.copied') : t('codex_reauth.copy_account')}
          </Button>
        </div>

        <div className={styles.oauthPanel}>
          <div className={styles.primaryActionRow}>
            <Button
              type="button"
              size="md"
              className={styles.primaryActionButton}
              onClick={openAuthUrl}
              disabled={!authUrl || status === 'loading' || status === 'success'}
            >
              <IconExternalLink size={16} />
              {t('codex_reauth.open_link')}
            </Button>
            {statusNode}
          </div>

          <div className={styles.linkPreviewRow}>
            <span className={styles.oauthLabel}>{t('codex_reauth.oauth_link_label')}</span>
            <span className={styles.linkPreview} title={authUrl || undefined}>
              {authUrl || '-'}
            </span>
          </div>

          <div className={styles.oauthActions}>
            <Button
              type="button"
              variant="secondary"
              size="sm"
              onClick={() => void copyText(authUrl, 'link')}
              disabled={!authUrl}
            >
              <IconCopy size={14} />
              {copiedTarget === 'link' ? t('codex_reauth.copied') : t('codex_reauth.copy_link')}
            </Button>
            <Button
              type="button"
              variant="secondary"
              size="sm"
              onClick={() => void loadAuthLink(true)}
              disabled={status === 'loading'}
              loading={status === 'loading'}
            >
              {!status || status !== 'loading' ? <IconRefreshCw size={14} /> : null}
              {linkRefreshed ? t('codex_reauth.link_refreshed') : t('codex_reauth.refresh_link')}
            </Button>
          </div>
        </div>

        <div className={styles.callbackSection}>
          <Input
            label={t('codex_reauth.callback_label')}
            placeholder={t('codex_reauth.callback_placeholder')}
            value={callbackUrl}
            onChange={(event) => {
              setCallbackUrl(event.target.value);
              setCallbackStatus(undefined);
              setCallbackError('');
            }}
            disabled={callbackSubmitting || status === 'success'}
          />
          <div className={styles.callbackActions}>
            <Button
              type="button"
              size="sm"
              onClick={() => void submitCallback()}
              loading={callbackSubmitting}
              disabled={callbackSubmitting || status === 'success'}
            >
              <IconCheck size={14} />
              {t('codex_reauth.submit_callback')}
            </Button>
          </div>
          {callbackStatus === 'error' ? (
            <div className={`${styles.status} ${styles.statusError}`}>
              {callbackError || t('codex_reauth.error')}
            </div>
          ) : null}
          {callbackStatus === 'success' && callbackError ? (
            <div className={`${styles.status} ${styles.statusWarning}`}>{callbackError}</div>
          ) : null}
        </div>
      </div>
    </Modal>
  );
}
