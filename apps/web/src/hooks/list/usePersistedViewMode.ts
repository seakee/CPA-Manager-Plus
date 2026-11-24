import { useCallback, useMemo, useState } from 'react';

export interface UsePersistedViewModeOptions<Mode extends string> {
  storageKey?: string;
  defaultMode: Mode;
  validModes?: readonly Mode[];
  compactFallbackMode?: Mode;
  isCompact?: boolean;
  value?: Mode;
  onChange?: (nextMode: Mode) => void;
}

export interface UsePersistedViewModeResult<Mode extends string> {
  mode: Mode;
  effectiveMode: Mode;
  setMode: (nextMode: Mode) => void;
  switcherHidden: boolean;
}

function readPersistedMode<Mode extends string>(
  storageKey: string | undefined,
  defaultMode: Mode,
  validModes?: readonly Mode[]
): Mode {
  if (!storageKey || typeof window === 'undefined') return defaultMode;
  try {
    const raw = window.localStorage.getItem(storageKey);
    if (!raw) return defaultMode;
    if (!validModes || validModes.includes(raw as Mode)) {
      return raw as Mode;
    }
    return defaultMode;
  } catch {
    return defaultMode;
  }
}

export function usePersistedViewMode<Mode extends string = 'table' | 'grid'>({
  storageKey,
  defaultMode,
  validModes,
  compactFallbackMode,
  isCompact = false,
  value: controlledValue,
  onChange,
}: UsePersistedViewModeOptions<Mode>): UsePersistedViewModeResult<Mode> {
  const [uncontrolledMode, setUncontrolledMode] = useState<Mode>(() =>
    readPersistedMode(storageKey, defaultMode, validModes)
  );
  const isControlled = controlledValue !== undefined;

  const validModesSignature = validModes ? validModes.join('\u0000') : null;
  const validModeSet = useMemo(
    () => (validModesSignature === null ? null : new Set(validModesSignature.split('\u0000'))),
    [validModesSignature]
  );

  const mode = controlledValue ?? uncontrolledMode;
  const effectiveMode = isCompact && compactFallbackMode ? compactFallbackMode : mode;

  const setMode = useCallback(
    (nextMode: Mode) => {
      if (validModeSet && !validModeSet.has(nextMode)) return;
      if (!isControlled) {
        setUncontrolledMode(nextMode);
      }
      if (storageKey && typeof window !== 'undefined') {
        try {
          window.localStorage.setItem(storageKey, nextMode);
        } catch {
          // Ignore storage quota or private-browsing failures.
        }
      }
      onChange?.(nextMode);
    },
    [isControlled, onChange, storageKey, validModeSet]
  );

  return {
    mode,
    effectiveMode,
    setMode,
    switcherHidden: isCompact,
  };
}
