import { useEffect, useId, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useParams } from 'react-router-dom';
import {
  CPAMP_HORIZONTAL_LOGO_ON_DARK_PNG_SRC_SET,
  CPAMP_HORIZONTAL_LOGO_ON_DARK_PNG_URL,
  CPAMP_HORIZONTAL_LOGO_PNG_SRC_SET,
  CPAMP_HORIZONTAL_LOGO_PNG_URL,
} from '@/assets/brand';
import { AppearanceToolbar } from '@/components/common/AppearanceToolbar';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import {
  formatCountdown,
  providerFlow,
  providerLabel,
} from '@/features/reconnect/model/reconnectFormat';
import {
  reconnectApi,
  reconnectErrorMessage,
  type ReconnectAttempt,
  type ReconnectLink,
} from '@/services/api/reconnect';
import styles from './ReconnectPage.module.scss';

const POLL_MS = 3000;

// The page is served by the Manager Server, so its API is on this origin.
const managerBase = () => window.location.origin;

/** Public page opened from a reconnect link; the token is the only credential. */
export function ReconnectPage() {
  const { t } = useTranslation();
  const { token = '' } = useParams();
  const [link, setLink] = useState<ReconnectLink | null>(null);
  const [linkError, setLinkError] = useState('');
  const [attempt, setAttempt] = useState<ReconnectAttempt | null>(null);
  const [callbackUrl, setCallbackUrl] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [done, setDone] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  const pasteLabelId = useId();

  useEffect(() => {
    reconnectApi
      .describeLink(managerBase(), token)
      .then(setLink)
      .catch((err) => setLinkError(reconnectErrorMessage(err)));
  }, [token]);

  useEffect(() => {
    if (!attempt) return;
    const id = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, [attempt]);

  const secondsLeft = attempt ? Math.max(0, Math.floor((attempt.deadlineMs - now) / 1000)) : 0;
  const attemptExpired = attempt !== null && secondsLeft === 0;
  const polling = attempt !== null && attempt.device && !attemptExpired && !done;

  useEffect(() => {
    if (!polling) return;
    const id = window.setInterval(async () => {
      try {
        const res = await reconnectApi.pollLink(managerBase(), token);
        if (res.status === 'ok') setDone(true);
      } catch (err) {
        setError(reconnectErrorMessage(err));
        setAttempt(null);
      }
    }, POLL_MS);
    return () => window.clearInterval(id);
  }, [polling, token]);

  const connect = async () => {
    setBusy(true);
    setError('');
    try {
      const next = await reconnectApi.startLink(managerBase(), token);
      setAttempt(next);
      setCallbackUrl('');
      setNow(Date.now());
      window.open(next.authUrl, '_blank', 'noopener');
    } catch (err) {
      setError(reconnectErrorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const submit = async () => {
    setBusy(true);
    setError('');
    try {
      await reconnectApi.submitLink(managerBase(), token, callbackUrl.trim());
      setDone(true);
    } catch (err) {
      setError(reconnectErrorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  // Same shell as the login page: centered glass card with the brand logo.
  const shell = (children: ReactNode) => (
    <div className={styles.page}>
      <AppearanceToolbar />
      <main className={styles.card}>
        <div className={styles.branding}>
          <img
            src={CPAMP_HORIZONTAL_LOGO_PNG_URL}
            srcSet={CPAMP_HORIZONTAL_LOGO_PNG_SRC_SET}
            alt="CPA Manager Plus"
            className={[styles.brandLogo, styles.brandLogoLight].join(' ')}
          />
          <img
            src={CPAMP_HORIZONTAL_LOGO_ON_DARK_PNG_URL}
            srcSet={CPAMP_HORIZONTAL_LOGO_ON_DARK_PNG_SRC_SET}
            alt="CPA Manager Plus"
            className={[styles.brandLogo, styles.brandLogoDark].join(' ')}
          />
        </div>
        {children}
      </main>
    </div>
  );

  if (!link && !linkError)
    return shell(
      <div className={styles.loading}>
        <LoadingSpinner />
      </div>
    );

  const flow = providerFlow(link?.provider ?? 'claude');
  const vars = {
    name: providerLabel(link?.provider ?? 'claude'),
    site: flow.site,
    approve: flow.approve,
    callback: flow.callback,
    email: link?.email ?? '',
  };

  if (!link || link.status !== 'pending') {
    let message =
      linkError ||
      t('reconnect_page.invalid', { defaultValue: 'This reconnect link is not valid.' });
    if (link?.status === 'expired') {
      message = t('reconnect_page.expired', {
        defaultValue:
          'This link has expired. If your {{name}} login still needs reconnecting, a new link will be sent to you.',
        ...vars,
      });
    } else if (link?.status === 'unused') {
      message = t('reconnect_page.unused', {
        defaultValue: 'This link has expired. Ask an administrator for a new one.',
      });
    } else if (link) {
      message = t('reconnect_page.closed', {
        defaultValue:
          'This link was already used or is no longer needed. Your {{name}} login is connected.',
        ...vars,
      });
    }
    return shell(
      <>
        <h1 className={styles.title}>
          {t('reconnect_page.title', { defaultValue: 'Reconnect {{name}}', ...vars })}
        </h1>
        <p className={styles.text}>{message}</p>
      </>
    );
  }

  if (done) {
    return shell(
      <div className={styles.done}>
        <h1 className={styles.title}>
          {t('reconnect_page.done_title', { defaultValue: '{{name}} reconnected', ...vars })}
        </h1>
        <p className={styles.text}>
          {t('reconnect_page.done', {
            defaultValue: 'Your {{name}} login for {{email}} works again. You can close this page.',
            ...vars,
          })}
        </p>
      </div>
    );
  }

  const urgent = secondsLeft > 0 && secondsLeft <= 60;

  return shell(
    <>
      <h1 className={styles.title}>
        {t('reconnect_page.title', { defaultValue: 'Reconnect {{name}}', ...vars })}
      </h1>
      <p className={styles.text}>
        {t('reconnect_page.intro', {
          defaultValue:
            'Sign in to {{site}} as {{email}} to connect your {{name}} login. It takes about a minute.',
          ...vars,
        })}
      </p>
      <ol className={styles.steps}>
        <li className={styles.step}>
          <span className={styles.stepTitle}>
            {t('reconnect_page.step_connect', {
              defaultValue: '1. Click Connect and sign in to {{site}} as {{email}}',
              ...vars,
            })}
          </span>
          {!attempt || attemptExpired ? (
            <Button fullWidth onClick={() => void connect()} loading={busy && !attempt}>
              {attemptExpired
                ? t('reconnect_page.connect_again', { defaultValue: 'Connect again' })
                : t('reconnect_page.connect', { defaultValue: 'Connect' })}
            </Button>
          ) : (
            <span className={styles.text}>
              {t('reconnect_page.opened', {
                defaultValue: '{{site}} opened in a new tab.',
                ...vars,
              })}{' '}
              <a href={attempt.authUrl} target="_blank" rel="noopener noreferrer">
                {t('reconnect_page.open_again', { defaultValue: 'Open it again' })}
              </a>
            </span>
          )}
          {attempt ? (
            <div className={styles.countdown} role="timer" aria-live="polite">
              <span>
                {attemptExpired
                  ? t('reconnect_page.time_up', { defaultValue: 'Time is up' })
                  : t('reconnect_page.time_left', { defaultValue: 'Time left' })}
              </span>
              <span
                className={[
                  styles.countdownTime,
                  urgent ? styles.urgent : '',
                  attemptExpired ? styles.expired : '',
                ].join(' ')}
              >
                {formatCountdown(secondsLeft)}
              </span>
            </div>
          ) : null}
          {attemptExpired ? (
            <p className={styles.text}>
              {t('reconnect_page.ran_out', {
                defaultValue:
                  'Time ran out. Click Connect again; you are already signed in, so it is quick.',
              })}
            </p>
          ) : null}
        </li>

        {flow.device ? (
          <li className={styles.step}>
            <span className={styles.stepTitle}>
              {t('reconnect_page.step_device', {
                defaultValue: '2. {{approve}} the request in {{site}}',
                ...vars,
              })}
            </span>
            {attempt?.userCode ? (
              <span className={styles.text}>
                {t('reconnect_page.check_code', {
                  defaultValue: 'Check that {{site}} shows this code:',
                  ...vars,
                })}{' '}
                <span className={styles.code}>{attempt.userCode}</span>
              </span>
            ) : null}
            <span className={styles.text}>
              {t('reconnect_page.no_copy', {
                defaultValue: 'Nothing to copy: this page notices the approval by itself.',
              })}
            </span>
          </li>
        ) : (
          <>
            <li className={styles.step}>
              <span className={styles.stepTitle}>
                {t('reconnect_page.step_approve', {
                  defaultValue: '2. Click {{approve}}',
                  ...vars,
                })}
              </span>
              <span className={styles.text}>
                {t('reconnect_page.redirect_note', {
                  defaultValue:
                    '{{site}} then sends you to a page that does not load (it starts with {{callback}}). That is expected.',
                  ...vars,
                })}
              </span>
            </li>
            <li className={styles.step}>
              <span className={styles.stepTitle} id={pasteLabelId}>
                {t('reconnect_page.step_paste', {
                  defaultValue: '3. Copy the full address from that tab and paste it here',
                })}
              </span>
              <Input
                aria-labelledby={pasteLabelId}
                value={callbackUrl}
                onChange={(event) => setCallbackUrl(event.target.value)}
                autoComplete="off"
                autoCorrect="off"
                autoCapitalize="none"
                spellCheck={false}
                placeholder={`${flow.callback}?code=...&state=...`}
                disabled={!attempt || attemptExpired}
              />
              <Button
                fullWidth
                onClick={() => void submit()}
                loading={busy && attempt !== null}
                disabled={!attempt || attemptExpired || !callbackUrl.trim()}
              >
                {t('reconnect_page.finish', { defaultValue: 'Finish reconnecting' })}
              </Button>
            </li>
          </>
        )}
      </ol>
      {error ? (
        <div className={styles.error} role="alert">
          {error}
        </div>
      ) : null}
    </>
  );
}
