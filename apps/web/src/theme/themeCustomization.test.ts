import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  applyThemeCustomization,
  DEFAULT_THEME_CUSTOMIZATION,
  getContrastColor,
  normalizeHexColor,
  THEME_PRESETS,
} from './themeCustomization';

describe('theme customization', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('ships unique named presets with two preview swatches each', () => {
    const values = THEME_PRESETS.map((preset) => preset.value);

    expect(values).toHaveLength(10);
    expect(new Set(values).size).toBe(values.length);
    expect(THEME_PRESETS.every((preset) => preset.swatches.length === 2)).toBe(true);
  });

  it('normalizes custom colors and picks a readable foreground', () => {
    expect(normalizeHexColor(' #ABCDEF ')).toBe('#abcdef');
    expect(normalizeHexColor('invalid')).toBe('#409eff');
    expect(getContrastColor('#ffffff')).toBe('#111827');
    expect(getContrastColor('#111827')).toBe('#ffffff');
  });

  it('applies all customization axes to the document root', () => {
    const setAttribute = vi.fn();
    const setProperty = vi.fn();
    vi.stubGlobal('document', {
      documentElement: {
        setAttribute,
        style: { setProperty },
      },
    });

    applyThemeCustomization({
      ...DEFAULT_THEME_CUSTOMIZATION,
      preset: 'custom',
      radius: 'xl',
      density: 'compact',
      font: 'serif',
      contentLayout: 'centered',
      customAccent: '#123456',
    });

    expect(setAttribute.mock.calls).toEqual([
      ['data-theme-preset', 'custom'],
      ['data-theme-radius', 'xl'],
      ['data-theme-density', 'compact'],
      ['data-theme-font', 'serif'],
      ['data-theme-content-layout', 'centered'],
    ]);
    expect(setProperty).toHaveBeenCalledWith('--theme-custom-primary', '#123456');
    expect(setProperty).toHaveBeenCalledWith('--theme-custom-contrast', '#ffffff');
  });
});
