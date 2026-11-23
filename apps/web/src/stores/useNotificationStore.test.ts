import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useNotificationStore } from './useNotificationStore';

describe('useNotificationStore', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    useNotificationStore.getState().clearAll();
  });

  afterEach(() => {
    useNotificationStore.getState().clearAll();
    vi.useRealTimers();
  });

  it('deduplicates identical active notifications, increments count, and refreshes duration', () => {
    const store = useNotificationStore.getState();
    store.showNotification('Saved', 'success');
    vi.advanceTimersByTime(1500);
    store.showNotification('Saved', 'success');

    const active = useNotificationStore.getState().notifications;
    expect(active).toHaveLength(1);
    expect(active[0].count).toBe(2);

    // Timer was reset at 1500ms so it stays alive past the initial 2400ms mark.
    vi.advanceTimersByTime(1500);
    expect(useNotificationStore.getState().notifications).toHaveLength(1);

    vi.advanceTimersByTime(1000);
    expect(useNotificationStore.getState().notifications).toHaveLength(0);
  });

  it('uses longer default duration for warnings/errors and pauses/resumes timers cleanly', () => {
    const store = useNotificationStore.getState();
    store.showNotification('Saved', 'success');
    store.showNotification('Gateway error', 'error');

    vi.advanceTimersByTime(1000);
    useNotificationStore.getState().pauseTimers();
    vi.advanceTimersByTime(10000);

    // Both remain visible while paused.
    expect(useNotificationStore.getState().notifications).toHaveLength(2);

    useNotificationStore.getState().resumeTimers();
    vi.advanceTimersByTime(1500);
    expect(useNotificationStore.getState().notifications.map((n) => n.message)).toEqual([
      'Gateway error',
    ]);

    vi.advanceTimersByTime(3000);
    expect(useNotificationStore.getState().notifications).toHaveLength(0);
  });

  it('caps active notifications at 6 by evicting lower-priority info/success entries first', () => {
    const store = useNotificationStore.getState();
    store.showNotification('Critical error', 'error');
    for (let i = 1; i <= 6; i += 1) {
      store.showNotification(`Success ${i}`, 'success');
    }

    const active = useNotificationStore.getState().notifications;
    expect(active).toHaveLength(6);
    expect(active.some((n) => n.message === 'Critical error')).toBe(true);
    expect(active.some((n) => n.message === 'Success 1')).toBe(false);
  });
});
