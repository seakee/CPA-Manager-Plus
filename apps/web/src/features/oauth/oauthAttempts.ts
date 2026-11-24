export interface OAuthAttempt {
  readonly signal: AbortSignal;
  isCurrent: () => boolean;
  /** True from poll() until a terminal result, a request error, or invalidation. */
  isPolling: () => boolean;
  invalidate: () => void;
  schedule: (callback: () => void, delay: number) => void;
  poll: <T>(
    request: () => Promise<T>,
    onResult: (result: T) => boolean | Promise<boolean>,
    onError: (error: unknown) => void | Promise<void>,
    delay: number
  ) => void;
}

export interface OAuthAttemptScheduler {
  setTimeout: (callback: () => void, delay: number) => number;
  clearTimeout: (timer: number) => void;
}

const createDefaultScheduler = (): OAuthAttemptScheduler => ({
  setTimeout: (callback, delay) => window.setTimeout(callback, delay),
  clearTimeout: (timer) => window.clearTimeout(timer),
});

/**
 * Per-key attempt manager with AbortController cancellation and chained polling.
 * Each key owns at most one active attempt and one scheduled timer; invalidating
 * an attempt aborts its signal and makes any in-flight settlement inert.
 */
export function createOAuthAttempts(
  scheduler: OAuthAttemptScheduler = createDefaultScheduler()
) {
  const attempts = new Map<string, OAuthAttempt>();

  const begin = (key: string): OAuthAttempt => {
    attempts.get(key)?.invalidate();
    let timer: number | undefined;
    let polling = false;
    let active = true;
    const controller = new AbortController();

    const attempt: OAuthAttempt = {
      signal: controller.signal,
      isCurrent: () => active && attempts.get(key) === attempt,
      isPolling: () => polling && attempt.isCurrent(),
      invalidate: () => {
        if (!active) return;
        active = false;
        polling = false;
        if (timer !== undefined) {
          scheduler.clearTimeout(timer);
          timer = undefined;
        }
        if (attempts.get(key) === attempt) {
          attempts.delete(key);
        }
        controller.abort();
      },
      schedule: (callback, delay) => {
        if (!attempt.isCurrent()) return;
        if (timer !== undefined) {
          scheduler.clearTimeout(timer);
        }
        timer = scheduler.setTimeout(() => {
          timer = undefined;
          if (attempt.isCurrent()) {
            callback();
          }
        }, delay);
      },
      poll: (request, onResult, onError, delay) => {
        if (!attempt.isCurrent()) return;
        polling = true;
        const tick = async () => {
          if (!attempt.isCurrent()) return;
          try {
            const result = await request();
            if (!attempt.isCurrent()) return;
            const shouldContinue = await onResult(result);
            if (!attempt.isCurrent()) return;
            if (shouldContinue) {
              attempt.schedule(() => void tick(), delay);
            } else {
              polling = false;
            }
          } catch (error: unknown) {
            if (!attempt.isCurrent()) return;
            polling = false;
            await onError(error);
          }
        };
        attempt.schedule(() => void tick(), delay);
      },
    };

    attempts.set(key, attempt);
    return attempt;
  };

  return {
    begin,
    get: (key: string) => attempts.get(key),
    invalidate: (key: string) => {
      attempts.get(key)?.invalidate();
    },
    invalidateAll: () => {
      for (const attempt of Array.from(attempts.values())) {
        attempt.invalidate();
      }
    },
  };
}
