import {
  type ChangeEvent,
  type ReactNode,
  type SVGProps,
  useEffect,
  useRef,
  useState,
} from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { useThemeCustomizationStore, useThemeStore, useVisualEffectsStore } from '@/stores';
import { THEME_PRESETS } from '@/theme/themeCustomization';
import type {
  Theme,
  ThemeContentLayout,
  ThemeDensity,
  ThemeFont,
  ThemeRadius,
  VisualEffectsMode,
} from '@/types';
import './ThemeStudio.scss';

const iconProps: SVGProps<SVGSVGElement> = {
  width: 18,
  height: 18,
  viewBox: '0 0 24 24',
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 1.8,
  strokeLinecap: 'round',
  strokeLinejoin: 'round',
  'aria-hidden': 'true',
  focusable: 'false',
};

const icons = {
  palette: (
    <svg {...iconProps}>
      <path d="M12 3a9 9 0 1 0 0 18h1.4a1.6 1.6 0 0 0 1.1-2.7 1.6 1.6 0 0 1 1.1-2.7H18a3 3 0 0 0 3-3A9.6 9.6 0 0 0 12 3Z" />
      <circle cx="7.5" cy="10.5" r=".8" fill="currentColor" stroke="none" />
      <circle cx="10" cy="7" r=".8" fill="currentColor" stroke="none" />
      <circle cx="14" cy="7" r=".8" fill="currentColor" stroke="none" />
      <circle cx="16.5" cy="10.5" r=".8" fill="currentColor" stroke="none" />
    </svg>
  ),
  close: (
    <svg {...iconProps}>
      <path d="m6 6 12 12M18 6 6 18" />
    </svg>
  ),
  reset: (
    <svg {...iconProps}>
      <path d="M3 12a9 9 0 1 0 3-6.7L3 8" />
      <path d="M3 3v5h5" />
    </svg>
  ),
  auto: (
    <svg {...iconProps}>
      <rect x="3" y="4" width="18" height="13" rx="2" />
      <path d="M8 21h8M12 17v4" />
    </svg>
  ),
  light: (
    <svg {...iconProps}>
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2M12 20v2M4.93 4.93l1.42 1.42M17.65 17.65l1.42 1.42M2 12h2M20 12h2M4.93 19.07l1.42-1.42M17.65 6.35l1.42-1.42" />
    </svg>
  ),
  dark: (
    <svg {...iconProps}>
      <path d="M20.6 14.5A8.5 8.5 0 0 1 9.5 3.4 9 9 0 1 0 20.6 14.5Z" />
    </svg>
  ),
  sparkle: (
    <svg {...iconProps}>
      <path d="m12 3 1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8L12 3Z" />
      <path d="M5 3v4M3 5h4M19 17v4M17 19h4" />
    </svg>
  ),
  performance: (
    <svg {...iconProps}>
      <path d="M4 14a8 8 0 0 1 16 0M12 14l4-5M8 14h8M5 19h14" />
    </svg>
  ),
  check: (
    <svg {...iconProps} width={15} height={15}>
      <path d="m5 12 4 4L19 6" />
    </svg>
  ),
};

const MODE_OPTIONS: ReadonlyArray<{ value: Theme; labelKey: string; icon: ReactNode }> = [
  { value: 'auto', labelKey: 'theme.auto', icon: icons.auto },
  { value: 'white', labelKey: 'theme.white', icon: icons.light },
  { value: 'dark', labelKey: 'theme.dark', icon: icons.dark },
];

const RADIUS_OPTIONS: ReadonlyArray<{ value: ThemeRadius; labelKey: string }> = [
  { value: 'default', labelKey: 'theme_studio.options.default' },
  { value: 'none', labelKey: 'theme_studio.radius.none' },
  { value: 'sm', labelKey: 'theme_studio.radius.sm' },
  { value: 'md', labelKey: 'theme_studio.radius.md' },
  { value: 'lg', labelKey: 'theme_studio.radius.lg' },
  { value: 'xl', labelKey: 'theme_studio.radius.xl' },
];

const DENSITY_OPTIONS: ReadonlyArray<{ value: ThemeDensity; labelKey: string }> = [
  { value: 'compact', labelKey: 'theme_studio.density.compact' },
  { value: 'default', labelKey: 'theme_studio.options.default' },
  { value: 'comfortable', labelKey: 'theme_studio.density.comfortable' },
];

const FONT_OPTIONS: ReadonlyArray<{ value: ThemeFont; labelKey: string; preview: string }> = [
  { value: 'system', labelKey: 'theme_studio.font.system', preview: 'Aa' },
  { value: 'modern', labelKey: 'theme_studio.font.modern', preview: 'Ag' },
  { value: 'serif', labelKey: 'theme_studio.font.serif', preview: 'Aa' },
];

const LAYOUT_OPTIONS: ReadonlyArray<{ value: ThemeContentLayout; labelKey: string }> = [
  { value: 'full', labelKey: 'theme_studio.layout.full' },
  { value: 'centered', labelKey: 'theme_studio.layout.centered' },
];

const EFFECT_OPTIONS: ReadonlyArray<{
  value: VisualEffectsMode;
  labelKey: string;
  descriptionKey: string;
  icon: ReactNode;
}> = [
  {
    value: 'full',
    labelKey: 'visual_effects.full',
    descriptionKey: 'theme_studio.effects.full_description',
    icon: icons.sparkle,
  },
  {
    value: 'reduced',
    labelKey: 'visual_effects.reduced',
    descriptionKey: 'theme_studio.effects.reduced_description',
    icon: icons.performance,
  },
];

interface ThemeStudioProps {
  triggerClassName?: string;
  triggerVariant?: 'ghost' | 'secondary';
}

export function ThemeStudio({
  triggerClassName = '',
  triggerVariant = 'ghost',
}: ThemeStudioProps = {}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const drawerRef = useRef<HTMLElement | null>(null);
  const closeRef = useRef<HTMLButtonElement | null>(null);

  const theme = useThemeStore((state) => state.theme);
  const setTheme = useThemeStore((state) => state.setTheme);
  const visualEffectsMode = useVisualEffectsStore((state) => state.mode);
  const setVisualEffectsMode = useVisualEffectsStore((state) => state.setMode);

  const preset = useThemeCustomizationStore((state) => state.preset);
  const radius = useThemeCustomizationStore((state) => state.radius);
  const density = useThemeCustomizationStore((state) => state.density);
  const font = useThemeCustomizationStore((state) => state.font);
  const contentLayout = useThemeCustomizationStore((state) => state.contentLayout);
  const customAccent = useThemeCustomizationStore((state) => state.customAccent);
  const setPreset = useThemeCustomizationStore((state) => state.setPreset);
  const setRadius = useThemeCustomizationStore((state) => state.setRadius);
  const setDensity = useThemeCustomizationStore((state) => state.setDensity);
  const setFont = useThemeCustomizationStore((state) => state.setFont);
  const setContentLayout = useThemeCustomizationStore((state) => state.setContentLayout);
  const setCustomAccent = useThemeCustomizationStore((state) => state.setCustomAccent);
  const resetCustomization = useThemeCustomizationStore((state) => state.resetCustomization);

  useEffect(() => {
    if (!open) {
      return;
    }

    const previouslyFocused = document.activeElement as HTMLElement | null;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        setOpen(false);
        return;
      }

      if (event.key !== 'Tab' || !drawerRef.current) {
        return;
      }

      const focusable = Array.from(
        drawerRef.current.querySelectorAll<HTMLElement>(
          'button:not([disabled]), input:not([disabled]), [href], [tabindex]:not([tabindex="-1"])'
        )
      );
      if (focusable.length === 0) {
        return;
      }

      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };

    document.body.classList.add('theme-studio-open');
    document.addEventListener('keydown', handleKeyDown);
    window.requestAnimationFrame(() => closeRef.current?.focus());

    return () => {
      document.body.classList.remove('theme-studio-open');
      document.removeEventListener('keydown', handleKeyDown);
      if (previouslyFocused?.isConnected) {
        previouslyFocused.focus();
      }
    };
  }, [open]);

  const handleReset = () => {
    setTheme('auto');
    setVisualEffectsMode('full');
    resetCustomization();
  };

  const handleCustomAccentChange = (event: ChangeEvent<HTMLInputElement>) => {
    setCustomAccent(event.target.value);
  };

  const drawer = open ? (
    <div className="theme-studio__layer">
      <button
        type="button"
        className="theme-studio__backdrop"
        onClick={() => setOpen(false)}
        aria-label={t('theme_studio.close')}
      />
      <aside
        className="theme-studio__drawer"
        ref={drawerRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="theme-studio-title"
        aria-describedby="theme-studio-description"
      >
        <header className="theme-studio__header">
          <div>
            <h2 id="theme-studio-title">{t('theme_studio.title')}</h2>
            <p id="theme-studio-description">{t('theme_studio.description')}</p>
          </div>
          <button
            ref={closeRef}
            type="button"
            className="theme-studio__icon-button"
            onClick={() => setOpen(false)}
            aria-label={t('theme_studio.close')}
          >
            {icons.close}
          </button>
        </header>

        <div className="theme-studio__body">
          <section className="theme-studio__section" aria-labelledby="theme-studio-mode">
            <div className="theme-studio__section-heading">
              <h3 id="theme-studio-mode">{t('theme_studio.mode')}</h3>
            </div>
            <div className="theme-studio__segmented theme-studio__segmented--three">
              {MODE_OPTIONS.map((option) => (
                <button
                  key={option.value}
                  type="button"
                  className={theme === option.value ? 'active' : ''}
                  onClick={() => setTheme(option.value)}
                  aria-pressed={theme === option.value}
                >
                  {option.icon}
                  <span>{t(option.labelKey)}</span>
                </button>
              ))}
            </div>
          </section>

          <section className="theme-studio__section" aria-labelledby="theme-studio-colors">
            <div className="theme-studio__section-heading">
              <h3 id="theme-studio-colors">{t('theme_studio.colors')}</h3>
              <span>{t('theme_studio.live_preview')}</span>
            </div>
            <div className="theme-studio__preset-grid">
              {THEME_PRESETS.map((option) => {
                const active = preset === option.value;
                return (
                  <button
                    key={option.value}
                    type="button"
                    className={`theme-studio__preset ${active ? 'active' : ''}`}
                    onClick={() => setPreset(option.value)}
                    aria-pressed={active}
                  >
                    <span className="theme-studio__swatches" aria-hidden="true">
                      <span style={{ background: option.swatches[0] }} />
                      <span style={{ background: option.swatches[1] }} />
                    </span>
                    <span className="theme-studio__preset-name">{t(option.labelKey)}</span>
                    <span className="theme-studio__check" aria-hidden="true">
                      {active ? icons.check : null}
                    </span>
                  </button>
                );
              })}
              <label
                className={`theme-studio__preset theme-studio__custom-color ${
                  preset === 'custom' ? 'active' : ''
                }`}
              >
                <span
                  className="theme-studio__custom-swatch"
                  style={{ background: customAccent }}
                  aria-hidden="true"
                />
                <span className="theme-studio__preset-name">
                  {t('theme_studio.presets.custom')}
                  <small>{customAccent.toUpperCase()}</small>
                </span>
                <input
                  type="color"
                  value={customAccent}
                  onChange={handleCustomAccentChange}
                  aria-label={t('theme_studio.custom_color')}
                />
              </label>
            </div>
          </section>

          <section className="theme-studio__section" aria-labelledby="theme-studio-radius">
            <div className="theme-studio__section-heading">
              <h3 id="theme-studio-radius">{t('theme_studio.radius.title')}</h3>
            </div>
            <div className="theme-studio__choice-grid theme-studio__choice-grid--three">
              {RADIUS_OPTIONS.map((option) => (
                <button
                  key={option.value}
                  type="button"
                  className={radius === option.value ? 'active' : ''}
                  onClick={() => setRadius(option.value)}
                  aria-pressed={radius === option.value}
                >
                  <span className={`theme-studio__radius-preview radius-${option.value}`} />
                  <span>{t(option.labelKey)}</span>
                </button>
              ))}
            </div>
          </section>

          <section className="theme-studio__section" aria-labelledby="theme-studio-font">
            <div className="theme-studio__section-heading">
              <h3 id="theme-studio-font">{t('theme_studio.font.title')}</h3>
            </div>
            <div className="theme-studio__choice-grid theme-studio__choice-grid--three">
              {FONT_OPTIONS.map((option) => (
                <button
                  key={option.value}
                  type="button"
                  className={`font-${option.value} ${font === option.value ? 'active' : ''}`}
                  onClick={() => setFont(option.value)}
                  aria-pressed={font === option.value}
                >
                  <strong>{option.preview}</strong>
                  <span>{t(option.labelKey)}</span>
                </button>
              ))}
            </div>
          </section>

          <section className="theme-studio__section" aria-labelledby="theme-studio-density">
            <div className="theme-studio__section-heading">
              <h3 id="theme-studio-density">{t('theme_studio.density.title')}</h3>
            </div>
            <div className="theme-studio__segmented theme-studio__segmented--three">
              {DENSITY_OPTIONS.map((option) => (
                <button
                  key={option.value}
                  type="button"
                  className={density === option.value ? 'active' : ''}
                  onClick={() => setDensity(option.value)}
                  aria-pressed={density === option.value}
                >
                  <span>{t(option.labelKey)}</span>
                </button>
              ))}
            </div>
          </section>

          <section className="theme-studio__section" aria-labelledby="theme-studio-layout">
            <div className="theme-studio__section-heading">
              <h3 id="theme-studio-layout">{t('theme_studio.layout.title')}</h3>
            </div>
            <div className="theme-studio__layout-grid">
              {LAYOUT_OPTIONS.map((option) => (
                <button
                  key={option.value}
                  type="button"
                  className={contentLayout === option.value ? 'active' : ''}
                  onClick={() => setContentLayout(option.value)}
                  aria-pressed={contentLayout === option.value}
                >
                  <span className={`theme-studio__layout-preview layout-${option.value}`}>
                    <i />
                    <i />
                  </span>
                  <span>{t(option.labelKey)}</span>
                </button>
              ))}
            </div>
          </section>

          <section className="theme-studio__section" aria-labelledby="theme-studio-effects">
            <div className="theme-studio__section-heading">
              <h3 id="theme-studio-effects">{t('visual_effects.switch')}</h3>
            </div>
            <div className="theme-studio__effects">
              {EFFECT_OPTIONS.map((option) => (
                <button
                  key={option.value}
                  type="button"
                  className={visualEffectsMode === option.value ? 'active' : ''}
                  onClick={() => setVisualEffectsMode(option.value)}
                  aria-pressed={visualEffectsMode === option.value}
                >
                  <span className="theme-studio__effect-icon">{option.icon}</span>
                  <span>
                    <strong>{t(option.labelKey)}</strong>
                    <small>{t(option.descriptionKey)}</small>
                  </span>
                  <span className="theme-studio__check" aria-hidden="true">
                    {visualEffectsMode === option.value ? icons.check : null}
                  </span>
                </button>
              ))}
            </div>
          </section>
        </div>

        <footer className="theme-studio__footer">
          <button type="button" className="theme-studio__reset" onClick={handleReset}>
            {icons.reset}
            <span>{t('theme_studio.reset')}</span>
          </button>
        </footer>
      </aside>
    </div>
  ) : null;

  return (
    <div className="theme-studio">
      <Button
        variant={triggerVariant}
        size="sm"
        iconOnly
        className={triggerClassName}
        onClick={() => setOpen(true)}
        title={t('theme_studio.open')}
        aria-label={t('theme_studio.open')}
        aria-haspopup="dialog"
        aria-expanded={open}
      >
        {icons.palette}
      </Button>
      {typeof document !== 'undefined' && drawer ? createPortal(drawer, document.body) : null}
    </div>
  );
}
