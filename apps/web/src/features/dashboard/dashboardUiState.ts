export type DashboardDisplayMode = 'simple' | 'advanced';

export const DEFAULT_DASHBOARD_DISPLAY_MODE: DashboardDisplayMode = 'advanced';
export const DASHBOARD_UI_STATE_STORAGE_KEY = 'dashboard.uiState';

export interface DashboardUiStateV1 {
  version: 1;
  displayMode: DashboardDisplayMode;
}

export const normalizeDashboardDisplayMode = (value: unknown): DashboardDisplayMode =>
  value === 'simple' || value === 'advanced' ? value : DEFAULT_DASHBOARD_DISPLAY_MODE;

export const readDashboardUiState = (): DashboardUiStateV1 => {
  if (typeof window === 'undefined') {
    return { version: 1, displayMode: DEFAULT_DASHBOARD_DISPLAY_MODE };
  }
  try {
    const storage = window.localStorage;
    if (!storage) {
      return { version: 1, displayMode: DEFAULT_DASHBOARD_DISPLAY_MODE };
    }
    const raw = storage.getItem(DASHBOARD_UI_STATE_STORAGE_KEY);
    if (!raw) {
      return { version: 1, displayMode: DEFAULT_DASHBOARD_DISPLAY_MODE };
    }
    const parsed: unknown = JSON.parse(raw);
    if (
      parsed !== null &&
      typeof parsed === 'object' &&
      'version' in parsed &&
      parsed.version === 1
    ) {
      const rawMode = 'displayMode' in parsed ? parsed.displayMode : undefined;
      return {
        version: 1,
        displayMode: normalizeDashboardDisplayMode(rawMode),
      };
    }
  } catch {
    // Capture SecurityError on window.localStorage getter, getItem errors, and malformed JSON.
  }
  return { version: 1, displayMode: DEFAULT_DASHBOARD_DISPLAY_MODE };
};

export const writeDashboardUiState = (displayMode: DashboardDisplayMode): void => {
  if (typeof window === 'undefined') {
    return;
  }
  try {
    const storage = window.localStorage;
    if (!storage) {
      return;
    }
    const payload: DashboardUiStateV1 = {
      version: 1,
      displayMode: normalizeDashboardDisplayMode(displayMode),
    };
    storage.setItem(DASHBOARD_UI_STATE_STORAGE_KEY, JSON.stringify(payload));
  } catch {
    // Capture SecurityError / QuotaExceededError and keep in-memory state active.
  }
};
