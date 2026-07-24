import type {
  ThemeContentLayout,
  ThemeDensity,
  ThemeFont,
  ThemePreset,
  ThemeRadius,
} from '@/types';

export interface ThemeCustomization {
  preset: ThemePreset;
  radius: ThemeRadius;
  density: ThemeDensity;
  font: ThemeFont;
  contentLayout: ThemeContentLayout;
  customAccent: string;
}

export const DEFAULT_THEME_CUSTOMIZATION: ThemeCustomization = {
  preset: 'default',
  radius: 'default',
  density: 'default',
  font: 'system',
  contentLayout: 'full',
  customAccent: '#409eff',
};

export const THEME_PRESETS: ReadonlyArray<{
  value: Exclude<ThemePreset, 'custom'>;
  labelKey: string;
  swatches: readonly [string, string];
}> = [
  { value: 'default', labelKey: 'theme_studio.presets.default', swatches: ['#409eff', '#7debdc'] },
  {
    value: 'anthropic',
    labelKey: 'theme_studio.presets.anthropic',
    swatches: ['#d97757', '#faf9f5'],
  },
  {
    value: 'underground',
    labelKey: 'theme_studio.presets.underground',
    swatches: ['#3f7c5c', '#a45b8d'],
  },
  {
    value: 'rose-garden',
    labelKey: 'theme_studio.presets.rose_garden',
    swatches: ['#df315b', '#f3a9b8'],
  },
  {
    value: 'lake-view',
    labelKey: 'theme_studio.presets.lake_view',
    swatches: ['#22b88a', '#2f8fa2'],
  },
  {
    value: 'sunset-glow',
    labelKey: 'theme_studio.presets.sunset_glow',
    swatches: ['#dc503c', '#f2a25b'],
  },
  {
    value: 'forest-whisper',
    labelKey: 'theme_studio.presets.forest_whisper',
    swatches: ['#267f72', '#52677f'],
  },
  {
    value: 'ocean-breeze',
    labelKey: 'theme_studio.presets.ocean_breeze',
    swatches: ['#3f6fe5', '#7358d8'],
  },
  {
    value: 'lavender-dream',
    labelKey: 'theme_studio.presets.lavender_dream',
    swatches: ['#9650cf', '#7fcbd3'],
  },
  {
    value: 'monochrome',
    labelKey: 'theme_studio.presets.monochrome',
    swatches: ['#202124', '#a7abb2'],
  },
];

const PRESET_VALUES = new Set<ThemePreset>([
  ...THEME_PRESETS.map((preset) => preset.value),
  'custom',
]);
const RADIUS_VALUES = new Set<ThemeRadius>(['default', 'none', 'sm', 'md', 'lg', 'xl']);
const DENSITY_VALUES = new Set<ThemeDensity>(['compact', 'default', 'comfortable']);
const FONT_VALUES = new Set<ThemeFont>(['system', 'modern', 'serif']);
const CONTENT_LAYOUT_VALUES = new Set<ThemeContentLayout>(['full', 'centered']);

export const isThemePreset = (value: unknown): value is ThemePreset =>
  typeof value === 'string' && PRESET_VALUES.has(value as ThemePreset);

export const isThemeRadius = (value: unknown): value is ThemeRadius =>
  typeof value === 'string' && RADIUS_VALUES.has(value as ThemeRadius);

export const isThemeDensity = (value: unknown): value is ThemeDensity =>
  typeof value === 'string' && DENSITY_VALUES.has(value as ThemeDensity);

export const isThemeFont = (value: unknown): value is ThemeFont =>
  typeof value === 'string' && FONT_VALUES.has(value as ThemeFont);

export const isThemeContentLayout = (value: unknown): value is ThemeContentLayout =>
  typeof value === 'string' && CONTENT_LAYOUT_VALUES.has(value as ThemeContentLayout);

export const normalizeHexColor = (value: unknown, fallback = '#409eff'): string => {
  if (typeof value !== 'string') {
    return fallback;
  }
  const normalized = value.trim().toLowerCase();
  return /^#[0-9a-f]{6}$/.test(normalized) ? normalized : fallback;
};

export const getContrastColor = (hexColor: string): '#111827' | '#ffffff' => {
  const color = normalizeHexColor(hexColor).slice(1);
  const red = Number.parseInt(color.slice(0, 2), 16);
  const green = Number.parseInt(color.slice(2, 4), 16);
  const blue = Number.parseInt(color.slice(4, 6), 16);
  const luminance = (0.2126 * red + 0.7152 * green + 0.0722 * blue) / 255;
  return luminance > 0.62 ? '#111827' : '#ffffff';
};

export const applyThemeCustomization = (customization: ThemeCustomization): void => {
  if (typeof document === 'undefined') {
    return;
  }

  const root = document.documentElement;
  root.setAttribute('data-theme-preset', customization.preset);
  root.setAttribute('data-theme-radius', customization.radius);
  root.setAttribute('data-theme-density', customization.density);
  root.setAttribute('data-theme-font', customization.font);
  root.setAttribute('data-theme-content-layout', customization.contentLayout);

  const accent = normalizeHexColor(customization.customAccent);
  root.style.setProperty('--theme-custom-primary', accent);
  root.style.setProperty('--theme-custom-contrast', getContrastColor(accent));
};
