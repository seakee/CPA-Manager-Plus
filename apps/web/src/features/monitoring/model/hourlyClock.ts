import { HOUR_MS, startOfLocalHourMs } from './range';

/** A wall-clock subscription independent from the user's data polling setting. */
export const subscribeHourlyClock = (
  onHour: (nowMs: number) => void,
  visibility:
    | Pick<Document, 'visibilityState' | 'addEventListener' | 'removeEventListener'>
    | undefined
) => {
  let boundary = startOfLocalHourMs(Date.now());
  let timer: ReturnType<typeof setTimeout>;
  const synchronize = () => {
    clearTimeout(timer);
    const nowMs = Date.now();
    const nextBoundary = startOfLocalHourMs(nowMs);
    if (nextBoundary !== boundary) {
      boundary = nextBoundary;
      onHour(nowMs);
    }
    timer = setTimeout(synchronize, Math.max(1, boundary + HOUR_MS - Date.now()));
  };
  const onVisibilityChange = () => {
    if (visibility?.visibilityState === 'visible') synchronize();
  };
  timer = setTimeout(synchronize, Math.max(1, boundary + HOUR_MS - Date.now()));
  visibility?.addEventListener('visibilitychange', onVisibilityChange);
  return () => {
    clearTimeout(timer);
    visibility?.removeEventListener('visibilitychange', onVisibilityChange);
  };
};
