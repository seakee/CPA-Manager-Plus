type PluginHostTheme = 'light' | 'dark';

interface PluginHostStyleOptions {
  theme: PluginHostTheme;
}

export interface PluginHostStyleBridge {
  refresh: (options?: Partial<PluginHostStyleOptions>) => boolean;
  disconnect: () => void;
}

// A plugin page is served from the Control origin but executes in an opaque
// sandbox origin, so the host can no longer read or write its document. Theme
// synchronization therefore travels over postMessage and is strictly
// opt-in: a plugin that never listens is unaffected.
export const PLUGIN_HOST_THEME_MESSAGE = 'cpamp:plugin-theme';
export const PLUGIN_HOST_THEME_PROTOCOL_VERSION = 1;

const PLUGIN_HOST_ATTR = 'data-cpamp-plugin-host';

const HOST_STYLE_FALLBACKS: Record<string, string> = {
  '--app-bg': '#eff2f7',
  '--app-surface': '#ffffff',
  '--app-surface-muted': '#f6faff',
  '--app-border': 'rgba(15, 23, 42, 0.08)',
  '--app-text-primary': '#2c3e50',
  '--app-text-regular': '#475569',
  '--app-text-muted': '#52606f',
  '--app-radius-md': '12px',
  '--bg-primary': 'var(--app-surface)',
  '--bg-secondary': 'var(--app-bg)',
  '--bg-tertiary': 'var(--app-surface-muted)',
  '--text-primary': 'var(--app-text-primary)',
  '--text-secondary': 'var(--app-text-regular)',
  '--text-tertiary': 'var(--app-text-muted)',
  '--border-color': 'var(--app-border)',
  '--border-hover': 'rgba(59, 130, 246, 0.28)',
  '--primary-color': '#3b82f6',
  '--primary-text': '#1d4ed8',
  '--primary-hover': '#60a5fa',
  '--primary-active': '#1d4ed8',
  '--primary-solid': '#2563eb',
  '--primary-contrast': '#ffffff',
  '--focus-bg': 'color-mix(in srgb, var(--primary-color) 6%, var(--bg-primary))',
  '--focus-border': 'color-mix(in srgb, var(--primary-color) 28%, var(--border-color))',
  '--focus-inset': 'color-mix(in srgb, var(--primary-color) 22%, transparent)',
  '--danger-color': '#b91c1c',
  '--warning-color': '#92400e',
  '--success-color': '#166534',
};

const CUSTOM_PROPERTY_NAME_RE = /^--[a-zA-Z0-9_-]+$/;

const collectHostVariables = (): Record<string, string> => {
  const vars: Record<string, string> = { ...HOST_STYLE_FALLBACKS };

  if (typeof window === 'undefined') return vars;

  const computed = window.getComputedStyle(document.documentElement);
  for (let index = 0; index < computed.length; index += 1) {
    const propertyName = computed.item(index);
    if (!CUSTOM_PROPERTY_NAME_RE.test(propertyName)) continue;

    const value = computed.getPropertyValue(propertyName).trim();
    if (value) {
      vars[propertyName] = value;
    }
  }

  return vars;
};

export interface PluginHostThemeMessage {
  type: typeof PLUGIN_HOST_THEME_MESSAGE;
  version: number;
  theme: PluginHostTheme;
  themeAttribute: 'dark' | 'white';
  hostAttribute: typeof PLUGIN_HOST_ATTR;
  variables: Record<string, string>;
}

export const buildPluginThemeMessage = (theme: PluginHostTheme): PluginHostThemeMessage => ({
  type: PLUGIN_HOST_THEME_MESSAGE,
  version: PLUGIN_HOST_THEME_PROTOCOL_VERSION,
  theme,
  themeAttribute: theme === 'dark' ? 'dark' : 'white',
  hostAttribute: PLUGIN_HOST_ATTR,
  variables: collectHostVariables(),
});

// Kept for the documented plugin-side snippet: a plugin chooses to adopt the
// host theme by applying this message to its own document.
export const buildPluginHostStyle = (theme: PluginHostTheme) => {
  const declarations = Object.entries(buildPluginThemeMessage(theme).variables)
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([name, value]) => `  ${name}: ${value};`)
    .join('\n');

  return `:root {\n${declarations}\n  color-scheme: ${theme === 'dark' ? 'dark' : 'light'};\n}`;
};

export const createPluginHostStyleBridge = (
  iframe: HTMLIFrameElement,
  options: PluginHostStyleOptions
): PluginHostStyleBridge | null => {
  let currentOptions = options;
  let observer: MutationObserver | null = null;
  let refreshTimer = 0;

  const post = () => {
    const target = iframe.contentWindow;
    if (!target) return false;
    // The sandboxed document has an opaque origin, so its actual origin is
    // "null" and cannot be named in targetOrigin. The message carries no
    // credential, only theme variables already visible in the admin UI.
    target.postMessage(buildPluginThemeMessage(currentOptions.theme), '*');
    return true;
  };

  const clearRefreshTimer = () => {
    if (!refreshTimer) return;
    window.clearTimeout(refreshTimer);
    refreshTimer = 0;
  };

  const scheduleRefresh = () => {
    if (refreshTimer) return;
    refreshTimer = window.setTimeout(() => {
      refreshTimer = 0;
      post();
    }, 0);
  };

  // The host cannot observe the plugin document, so it re-posts whenever the
  // frame finishes a navigation. A plugin that reloads itself receives the
  // theme again without any host-side document access.
  const observeNavigation = () => {
    if (typeof MutationObserver === 'undefined') return;
    const observeTarget = iframe.parentElement;
    if (!observeTarget) return;
    observer = new MutationObserver(() => {
      if (iframe.contentWindow) scheduleRefresh();
    });
    observer.observe(observeTarget, { childList: true, subtree: true });
    window.addEventListener('pageshow', scheduleRefresh);
  };

  const disconnect = () => {
    clearRefreshTimer();
    observer?.disconnect();
    observer = null;
    window.removeEventListener('pageshow', scheduleRefresh);
  };

  if (!post()) {
    return null;
  }
  observeNavigation();

  return {
    refresh: (nextOptions) => {
      currentOptions = { ...currentOptions, ...nextOptions };
      return post();
    },
    disconnect,
  };
};
