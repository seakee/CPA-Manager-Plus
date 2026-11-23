import { useEffect, useRef, useState, type FocusEvent, type KeyboardEvent } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { useNotificationStore } from '@/stores';
import { IconCheck, IconInfo, IconTriangleAlert, IconX } from '@/components/ui/icons';
import type { Notification, NotificationType } from '@/types';

interface AnimatedNotification extends Notification {
  isExiting?: boolean;
}

const ANIMATION_DURATION = 300; // ms

function renderNotificationIcon(type: NotificationType) {
  switch (type) {
    case 'success':
      return <IconCheck size={15} />;
    case 'warning':
    case 'error':
      return <IconTriangleAlert size={15} />;
    default:
      return <IconInfo size={15} />;
  }
}

export function NotificationContainer() {
  const { t } = useTranslation();
  const store = useNotificationStore();
  const notifications = store.notifications;
  const removeNotification = store.removeNotification;
  const clearAll = store.clearAll;
  const pauseTimers = store.pauseTimers;
  const resumeTimers = store.resumeTimers;
  const [animatedNotifications, setAnimatedNotifications] = useState<AnimatedNotification[]>([]);
  const [expanded, setExpanded] = useState(false);
  const prevNotificationsRef = useRef<Notification[]>([]);
  const exitTimersRef = useRef<Map<string, Parameters<typeof clearTimeout>[0]>>(new Map());

  useEffect(() => {
    const prevNotifications = prevNotificationsRef.current;
    const prevIds = new Set(prevNotifications.map((n) => n.id));
    const currentIds = new Set(notifications.map((n) => n.id));
    const incomingById = new Map(notifications.map((n) => [n.id, n]));

    const newNotifications = notifications.filter((n) => !prevIds.has(n.id));

    const removedIds = new Set(
      prevNotifications.filter((n) => !currentIds.has(n.id)).map((n) => n.id)
    );

    setAnimatedNotifications((prev) => {
      const remainingIds = new Set<string>();
      let updated = prev.map((n) => {
        const latest = incomingById.get(n.id);
        if (latest) {
          remainingIds.add(n.id);
          return { ...latest, isExiting: false };
        }
        if (removedIds.has(n.id)) {
          return { ...n, isExiting: true };
        }
        return n;
      });

      // Re-order active items to match incoming store order (newest appended at end),
      // while keeping exiting items in place briefly for exit animation.
      newNotifications.forEach((n) => {
        if (!remainingIds.has(n.id)) {
          updated.push({ ...n, isExiting: false });
        }
      });

      const activeById = new Map(notifications.map((n, index) => [n.id, index]));
      updated = updated
        .filter((n) => currentIds.has(n.id) || n.isExiting)
        .sort((a, b) => {
          const ia = activeById.get(a.id);
          const ib = activeById.get(b.id);
          if (ia !== undefined && ib !== undefined) return ia - ib;
          return 0;
        });

      return updated;
    });

    if (removedIds.size > 0) {
      removedIds.forEach((id) => {
        clearTimeout(exitTimersRef.current.get(id));
        const timerId = setTimeout(() => {
          exitTimersRef.current.delete(id);
          setAnimatedNotifications((prev) => prev.filter((n) => n.id !== id));
        }, ANIMATION_DURATION);
        exitTimersRef.current.set(id, timerId);
      });
    }

    if (notifications.length <= 1) {
      setExpanded(false);
      resumeTimers?.();
    }

    prevNotificationsRef.current = notifications;
  }, [notifications, resumeTimers]);

  useEffect(() => {
    const timers = exitTimersRef.current;
    return () => {
      timers.forEach((timerId) => clearTimeout(timerId));
      timers.clear();
    };
  }, []);

  const handleClose = (id: string) => {
    setAnimatedNotifications((prev) =>
      prev.map((n) => (n.id === id ? { ...n, isExiting: true } : n))
    );

    clearTimeout(exitTimersRef.current.get(id));
    const timerId = setTimeout(() => {
      exitTimersRef.current.delete(id);
      removeNotification(id);
    }, ANIMATION_DURATION);
    exitTimersRef.current.set(id, timerId);
  };

  const handleClearAll = () => {
    setExpanded(false);
    if (clearAll) {
      clearAll();
      return;
    }
    animatedNotifications.forEach((n) => removeNotification(n.id));
  };

  const handleMouseEnter = () => {
    pauseTimers?.();
    if (animatedNotifications.length > 1) {
      setExpanded(true);
    }
  };

  const handleMouseLeave = () => {
    setExpanded(false);
    resumeTimers?.();
  };

  const handleFocusCapture = () => {
    pauseTimers?.();
    if (animatedNotifications.length > 1) {
      setExpanded(true);
    }
  };

  const handleBlurCapture = (event: FocusEvent<HTMLDivElement>) => {
    const nextTarget = event.relatedTarget as Node | null;
    if (nextTarget && event.currentTarget.contains(nextTarget)) {
      return;
    }
    setExpanded(false);
    resumeTimers?.();
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'Escape') {
      handleClearAll();
    }
  };

  if (!animatedNotifications.length) return null;

  const ordered = [...animatedNotifications].reverse();
  const isStacked = ordered.length > 1;
  const isExpanded = isStacked && expanded;
  const hiddenCount = isStacked && !isExpanded ? ordered.length - 1 : 0;

  const content = (
    <div
      className={`notification-container${isStacked ? ' is-stacked' : ''}${isExpanded ? ' is-expanded' : ''}`}
      data-testid="notification-container"
      role="region"
      aria-live="polite"
    >
      <div
        className="notification-stack"
        onMouseEnter={handleMouseEnter}
        onMouseLeave={handleMouseLeave}
        onFocusCapture={handleFocusCapture}
        onBlurCapture={handleBlurCapture}
        onKeyDown={handleKeyDown}
      >
        {ordered.map((notification, index) => {
          const stackLayerClass =
            !isExpanded && isStacked && index > 0
              ? index === 1
                ? ' is-peek-1'
                : index === 2
                  ? ' is-peek-2'
                  : ' is-hidden-layer'
              : '';
          const count = notification.count ?? 1;
          return (
            <div
              key={notification.id}
              className={`notification ${notification.type} ${notification.isExiting ? 'exiting' : 'entering'}${index === 0 ? ' is-front' : ''}${stackLayerClass}`}
              data-testid={`notification-item-${notification.id}`}
            >
              <span className="notification-icon" aria-hidden="true">
                {renderNotificationIcon(notification.type)}
              </span>
              <div className="message">
                {notification.renderMessage
                  ? notification.renderMessage(t)
                  : notification.message}
              </div>
              {count > 1 && (
                <span
                  className="notification-repeat-badge"
                  data-testid="notification-repeat-badge"
                >
                  ×{count}
                </span>
              )}
              {index === 0 && hiddenCount > 0 && (
                <button
                  type="button"
                  className="notification-stack-more"
                  data-testid="notification-stack-more"
                  onClick={() => {
                    pauseTimers?.();
                    setExpanded(true);
                  }}
                >
                  +{hiddenCount}
                </button>
              )}
              <button
                type="button"
                className="close-btn"
                onClick={() => handleClose(notification.id)}
                aria-label={t('common.close')}
              >
                <IconX size={14} />
              </button>
            </div>
          );
        })}
        {isExpanded && (
          <div className="notification-stack-toolbar" data-testid="notification-stack-toolbar">
            <button
              type="button"
              className="notification-clear-all"
              data-testid="notification-clear-all"
              onClick={handleClearAll}
            >
              {t('notification.clear_all')}
            </button>
          </div>
        )}
      </div>
    </div>
  );

  if (typeof document === 'undefined') return content;

  return createPortal(content, document.body);
}
