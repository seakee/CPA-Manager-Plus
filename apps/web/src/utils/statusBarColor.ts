import { readThemeToken } from '@/utils/dataPalette';

type RgbStop = { r: number; g: number; b: number };

const DEFAULT_STOPS: readonly [RgbStop, RgbStop, RgbStop] = [
  { r: 239, g: 68, b: 68 },
  { r: 250, g: 204, b: 21 },
  { r: 34, g: 197, b: 94 },
];

const parseRgb = (raw: string, fallback: RgbStop): RgbStop => {
  const value = raw.trim();
  const hex6 = /^#([\da-f]{2})([\da-f]{2})([\da-f]{2})$/i.exec(value);
  if (hex6) {
    return {
      r: Number.parseInt(hex6[1], 16),
      g: Number.parseInt(hex6[2], 16),
      b: Number.parseInt(hex6[3], 16),
    };
  }
  const rgb = /^rgba?\(\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})/i.exec(value);
  if (rgb) {
    return {
      r: Number.parseInt(rgb[1], 10),
      g: Number.parseInt(rgb[2], 10),
      b: Number.parseInt(rgb[3], 10),
    };
  }
  return fallback;
};

/**
 * Interpolate success rate (0..1) across themes.scss status-bar stops (low -> mid -> high).
 */
export const interpolateStatusBarColor = (rate: number): string => {
  const stops: readonly [RgbStop, RgbStop, RgbStop] = [
    parseRgb(readThemeToken('--status-bar-low', ''), DEFAULT_STOPS[0]),
    parseRgb(readThemeToken('--status-bar-mid', ''), DEFAULT_STOPS[1]),
    parseRgb(readThemeToken('--status-bar-high', ''), DEFAULT_STOPS[2]),
  ];
  const t = Math.max(0, Math.min(1, rate));
  const segment = t < 0.5 ? 0 : 1;
  const localT = segment === 0 ? t * 2 : (t - 0.5) * 2;
  const from = stops[segment];
  const to = stops[segment + 1];
  const r = Math.round(from.r + (to.r - from.r) * localT);
  const g = Math.round(from.g + (to.g - from.g) * localT);
  const b = Math.round(from.b + (to.b - from.b) * localT);
  return `rgb(${r}, ${g}, ${b})`;
};
