import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import { Link, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useAuthStore } from '@/stores';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { isDemoMode } from '@/features/demo/demoMode';
import { Button } from '@/components/ui/Button';
import { IconArrowUpFromLine, IconChevronRight, IconX } from '@/components/ui/icons';
import {
  managerUpdateRequest,
  type ReleaseInfo,
  type UpdateChannel,
  type UpdateStatus,
} from './managerUpdateApi';
import styles from './ManagerUpdates.module.scss';

interface Updates {
  status: UpdateStatus | null;
  available: boolean;
  busy: boolean;
  error: boolean;
  check: () => Promise<void>;
  setChannel: (channel: UpdateChannel) => Promise<void>;
}
const Context = createContext<Updates>({
  status: null,
  available: false,
  busy: false,
  error: false,
  check: async () => {},
  setChannel: async () => {},
});
// 与概览和更新页共享同一个生命周期。
// eslint-disable-next-line react-refresh/only-export-components
export const useManagerUpdates = () => useContext(Context);

export function ManagerUpdates({ children }: { children: ReactNode }) {
  const { t, i18n } = useTranslation();
  const { pathname } = useLocation();
  const onUpdatesPage = pathname.replace(/\/+$/, '') === '/system/updates';
  const viewingUpdates = useRef(onUpdatesPage);
  const updatePageVisits = useRef(0);
  const auth = useAuthStore((s) => s.isAuthenticated);
  const key = useAuthStore((s) => s.managementKey);
  const availability = usePanelFeatureAvailability();
  const base = availability.managerServiceBase;
  const available =
    auth && availability.managerServiceAvailable && !!base && !(__DEMO_SITE__ && isDemoMode());
  const [status, setStatus] = useState<UpdateStatus | null>(null);
  const [notification, setNotification] = useState<ReleaseInfo | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(false);
  const generation = useRef(0);
  const requestSequence = useRef(0);
  const channelGeneration = useRef(0);
  const latestStatus = useRef<UpdateStatus | null>(null);
  const claiming = useRef<object | null>(null);
  const mutation = useRef<object | null>(null);

  useLayoutEffect(() => {
    viewingUpdates.current = onUpdatesPage;
    if (onUpdatesPage) {
      updatePageVisits.current += 1;
      setNotification(null);
    }
  }, [onUpdatesPage]);

  const refresh = useCallback(
    async (suffix = '', method = 'GET', body?: unknown) => {
      if (!available || (method === 'GET' && mutation.current)) return;
      const current = generation.current;
      const request = ++requestSequence.current;
      const channel = channelGeneration.current;
      try {
        const next = await managerUpdateRequest<UpdateStatus>(base, key, suffix, method, body);
        if (current !== generation.current || request !== requestSequence.current) return;
        latestStatus.current = next;
        setStatus(next);
        setError(false);
        const eligible = next.state === 'update_available' && !next.stale && !next.last_error;
        setNotification((old) =>
          eligible &&
          !viewingUpdates.current &&
          old?.release.version === next.target?.release.version
            ? old
            : null
        );
        if (eligible && document.visibilityState === 'visible' && !claiming.current) {
          const claim = {};
          const visit = updatePageVisits.current;
          const claimedOnUpdatesPage = viewingUpdates.current;
          claiming.current = claim;
          try {
            // 更新页也领取一次，记录该版本已被查看，但不叠加浮层。
            const info = await managerUpdateRequest<ReleaseInfo | null>(
              base,
              key,
              '/notification',
              'POST'
            );
            const latest = latestStatus.current;
            if (
              current === generation.current &&
              channel === channelGeneration.current &&
              visit === updatePageVisits.current &&
              !claimedOnUpdatesPage &&
              !viewingUpdates.current &&
              info &&
              latest?.state === 'update_available' &&
              !latest.stale &&
              !latest.last_error &&
              latest.target?.release.version === info.release.version
            )
              setNotification(info);
          } finally {
            if (claiming.current === claim) claiming.current = null;
          }
        }
      } catch {
        if (current === generation.current && request === requestSequence.current) {
          latestStatus.current = null;
          setNotification(null);
          setError(true);
        }
      }
    },
    [available, base, key]
  );

  useEffect(() => {
    generation.current += 1;
    latestStatus.current = null;
    claiming.current = null;
    mutation.current = null;
    setStatus(null);
    setNotification(null);
    setError(false);
    setBusy(false);
    void refresh();
    const interval = window.setInterval(() => {
      if (document.visibilityState === 'visible') void refresh();
    }, 60_000);
    const visible = () => {
      if (document.visibilityState === 'visible') void refresh();
    };
    document.addEventListener('visibilitychange', visible);
    return () => {
      generation.current += 1;
      window.clearInterval(interval);
      document.removeEventListener('visibilitychange', visible);
    };
  }, [refresh]);

  const mutate = async (suffix: string, method: string, data?: unknown) => {
    if (!available || mutation.current) return;
    const pending = {};
    mutation.current = pending;
    if (suffix === '/channel') {
      channelGeneration.current += 1;
      latestStatus.current = null;
      setNotification(null);
    }
    setBusy(true);
    try {
      await refresh(suffix, method, data);
    } finally {
      if (mutation.current === pending) {
        mutation.current = null;
        setBusy(false);
      }
    }
  };
  const dismiss = () => {
    const tag = notification?.release.version;
    setNotification(null);
    if (tag)
      void managerUpdateRequest(base, key, '/dismiss', 'POST', { version: tag }).catch(() => {});
  };
  const language = i18n.language.startsWith('zh') ? 'zh' : 'en';
  return (
    <Context.Provider
      value={{
        status,
        available,
        busy,
        error,
        check: () => mutate('/check', 'POST'),
        setChannel: (channel) => mutate('/channel', 'PUT', { channel_preference: channel }),
      }}
    >
      {children}
      {available && !error && !onUpdatesPage && notification && (
        <aside className={styles.notice} role="status" aria-live="polite">
          <Link
            to="/system/updates"
            className={styles.noticeLink}
            aria-label={t('manager_updates.view_version', {
              version: notification.release.version,
            })}
            onClick={dismiss}
          >
            <span className={styles.noticeIcon} aria-hidden="true">
              <IconArrowUpFromLine size={18} />
            </span>
            <div className={styles.noticeBody}>
              <strong className={styles.noticeTitle}>
                {t('manager_updates.new_version', { version: notification.release.version })}
              </strong>
              <p className={styles.noticeSummary}>
                {notification.content.summary[language] || notification.content.summary.en}
              </p>
              <span className={styles.noticeAction}>
                {t('manager_updates.view_update')}
                <IconChevronRight size={14} aria-hidden="true" />
              </span>
            </div>
          </Link>
          <Button
            type="button"
            size="sm"
            variant="ghost"
            iconOnly
            className={styles.close}
            onClick={dismiss}
            aria-label={t('manager_updates.dismiss')}
            title={t('manager_updates.dismiss')}
          >
            <IconX size={16} aria-hidden="true" />
          </Button>
        </aside>
      )}
    </Context.Provider>
  );
}
