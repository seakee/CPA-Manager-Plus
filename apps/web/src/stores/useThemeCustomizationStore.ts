import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import type {
  ThemeContentLayout,
  ThemeDensity,
  ThemeFont,
  ThemePreset,
  ThemeRadius,
} from '@/types';
import { STORAGE_KEY_THEME_CUSTOMIZATION } from '@/utils/constants';
import {
  applyThemeCustomization,
  DEFAULT_THEME_CUSTOMIZATION,
  isThemeContentLayout,
  isThemeDensity,
  isThemeFont,
  isThemePreset,
  isThemeRadius,
  normalizeHexColor,
  type ThemeCustomization,
} from '@/theme/themeCustomization';

interface ThemeCustomizationState extends ThemeCustomization {
  setPreset: (preset: ThemePreset) => void;
  setRadius: (radius: ThemeRadius) => void;
  setDensity: (density: ThemeDensity) => void;
  setFont: (font: ThemeFont) => void;
  setContentLayout: (contentLayout: ThemeContentLayout) => void;
  setCustomAccent: (customAccent: string) => void;
  resetCustomization: () => void;
  initializeThemeCustomization: () => void;
}

const pickCustomization = (state: ThemeCustomizationState): ThemeCustomization => ({
  preset: state.preset,
  radius: state.radius,
  density: state.density,
  font: state.font,
  contentLayout: state.contentLayout,
  customAccent: state.customAccent,
});

const normalizePersistedCustomization = (
  persistedState: Partial<ThemeCustomizationState> | undefined
): ThemeCustomization => ({
  preset: isThemePreset(persistedState?.preset)
    ? persistedState.preset
    : DEFAULT_THEME_CUSTOMIZATION.preset,
  radius: isThemeRadius(persistedState?.radius)
    ? persistedState.radius
    : DEFAULT_THEME_CUSTOMIZATION.radius,
  density: isThemeDensity(persistedState?.density)
    ? persistedState.density
    : DEFAULT_THEME_CUSTOMIZATION.density,
  font: isThemeFont(persistedState?.font) ? persistedState.font : DEFAULT_THEME_CUSTOMIZATION.font,
  contentLayout: isThemeContentLayout(persistedState?.contentLayout)
    ? persistedState.contentLayout
    : DEFAULT_THEME_CUSTOMIZATION.contentLayout,
  customAccent: normalizeHexColor(
    persistedState?.customAccent,
    DEFAULT_THEME_CUSTOMIZATION.customAccent
  ),
});

export const useThemeCustomizationStore = create<ThemeCustomizationState>()(
  persist(
    (set, get) => ({
      ...DEFAULT_THEME_CUSTOMIZATION,

      setPreset: (preset) => {
        const next = { ...pickCustomization(get()), preset };
        applyThemeCustomization(next);
        set({ preset });
      },

      setRadius: (radius) => {
        const next = { ...pickCustomization(get()), radius };
        applyThemeCustomization(next);
        set({ radius });
      },

      setDensity: (density) => {
        const next = { ...pickCustomization(get()), density };
        applyThemeCustomization(next);
        set({ density });
      },

      setFont: (font) => {
        const next = { ...pickCustomization(get()), font };
        applyThemeCustomization(next);
        set({ font });
      },

      setContentLayout: (contentLayout) => {
        const next = { ...pickCustomization(get()), contentLayout };
        applyThemeCustomization(next);
        set({ contentLayout });
      },

      setCustomAccent: (customAccent) => {
        const normalizedAccent = normalizeHexColor(customAccent, get().customAccent);
        const next = {
          ...pickCustomization(get()),
          preset: 'custom' as const,
          customAccent: normalizedAccent,
        };
        applyThemeCustomization(next);
        set({ preset: 'custom', customAccent: normalizedAccent });
      },

      resetCustomization: () => {
        applyThemeCustomization(DEFAULT_THEME_CUSTOMIZATION);
        set(DEFAULT_THEME_CUSTOMIZATION);
      },

      initializeThemeCustomization: () => {
        applyThemeCustomization(pickCustomization(get()));
      },
    }),
    {
      name: STORAGE_KEY_THEME_CUSTOMIZATION,
      merge: (persistedState, currentState) => ({
        ...currentState,
        ...normalizePersistedCustomization(
          persistedState as Partial<ThemeCustomizationState> | undefined
        ),
      }),
    }
  )
);
