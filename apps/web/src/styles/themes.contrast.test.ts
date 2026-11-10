import { describe, expect, it } from 'vitest';

/**
 * Contrast contract for the shared theme tokens.
 *
 * The audit that caught the v2 walkthrough findings measured computed styles in
 * the browser, which is not reproducible in CI. This test instead pins the
 * behaviour at the token level: every text-role token has to clear WCAG AA on
 * the real surface set of its own theme (canvas, card, muted card, accent
 * tints), the quaternary tier has to clear the glyph threshold, and each badge
 * text token has to clear AA on its own badge background.
 *
 * Surfaces are derived from the theme itself so the assertions follow the tokens
 * instead of duplicating their values.
 */

interface Rgba {
  r: number;
  g: number;
  b: number;
  a: number;
}

type Declarations = Map<string, string>;

const AA_TEXT = 4.5;
const AA_GLYPH = 3;

// apps/web/tsconfig.json targets browser globals without @types/node, so the
// node builtins are imported lazily and typed locally instead of statically.
const readStylesheet = async (url: URL): Promise<string> => {
  const fsSpecifier = 'node:fs';
  const { readFileSync } = (await import(fsSpecifier)) as {
    readFileSync: (path: string | URL, encoding: 'utf8') => string;
  };
  return readFileSync(url, 'utf8');
};

const THEMES_URL = new URL('./themes.scss', import.meta.url);

const stripComments = (source: string): string => source.replace(/\/\*[\s\S]*?\*\//g, '');

const blockBody = (source: string, selector: string): string => {
  const start = source.indexOf(selector);
  if (start < 0) throw new Error(`missing theme block ${selector}`);
  const open = source.indexOf('{', start);
  let depth = 0;
  for (let index = open; index < source.length; index += 1) {
    if (source[index] === '{') depth += 1;
    if (source[index] === '}') {
      depth -= 1;
      if (depth === 0) return source.slice(open + 1, index);
    }
  }
  throw new Error(`unterminated theme block ${selector}`);
};

const declarationsOf = (block: string): Declarations => {
  const declarations: Declarations = new Map();
  for (const match of block.matchAll(/(--[A-Za-z0-9_-]+)\s*:\s*([^;{}]+);/g)) {
    declarations.set(match[1], match[2].replace(/\s+/g, ' ').trim());
  }
  return declarations;
};

const themeDeclarations = (source: string, themeSelector: string): Declarations => {
  const merged = new Map(declarationsOf(blockBody(source, ':root')));
  for (const [name, value] of declarationsOf(blockBody(source, themeSelector))) {
    merged.set(name, value);
  }
  return merged;
};

const parseColor = (value: string): Rgba | null => {
  const hex = value.match(/^#([0-9a-f]{3,8})$/i);
  if (hex) {
    const digits = hex[1];
    if (digits.length === 3 || digits.length === 4) {
      const [r, g, b, a] = digits.split('');
      return {
        r: Number.parseInt(r + r, 16),
        g: Number.parseInt(g + g, 16),
        b: Number.parseInt(b + b, 16),
        a: a ? Number.parseInt(a + a, 16) / 255 : 1,
      };
    }
    if (digits.length === 6 || digits.length === 8) {
      return {
        r: Number.parseInt(digits.slice(0, 2), 16),
        g: Number.parseInt(digits.slice(2, 4), 16),
        b: Number.parseInt(digits.slice(4, 6), 16),
        a: digits.length === 8 ? Number.parseInt(digits.slice(6, 8), 16) / 255 : 1,
      };
    }
    return null;
  }
  const rgb = value.match(
    /^rgba?\(\s*(\d+)\s*[, ]\s*(\d+)\s*[, ]\s*(\d+)\s*(?:[,/]\s*([\d.]+))?\s*\)$/i
  );
  if (rgb) {
    return {
      r: Number(rgb[1]),
      g: Number(rgb[2]),
      b: Number(rgb[3]),
      a: rgb[4] === undefined ? 1 : Number(rgb[4]),
    };
  }
  return null;
};

const resolveValue = (declarations: Declarations, value: string, depth = 0): Rgba | null => {
  if (depth > 12) return null;
  const direct = parseColor(value.trim());
  if (direct) return direct;
  const reference = value.trim().match(/^var\(\s*(--[A-Za-z0-9_-]+)\s*(?:,\s*([\s\S]+))?\)$/);
  if (!reference) return null;
  const [, name, fallback] = reference;
  const resolved = declarations.get(name);
  const nested = resolved ? resolveValue(declarations, resolved, depth + 1) : null;
  if (nested) return nested;
  return fallback ? resolveValue(declarations, fallback, depth + 1) : null;
};

const tokenColor = (declarations: Declarations, name: string): Rgba => {
  const color = resolveValue(declarations, `var(${name})`);
  if (!color) throw new Error(`cannot resolve ${name}`);
  return color;
};

const over = (foreground: Rgba, background: Rgba): Rgba => {
  const base = background.a === 1 ? background : { ...background, a: 1 };
  const alpha = foreground.a;
  return {
    r: foreground.r * alpha + base.r * (1 - alpha),
    g: foreground.g * alpha + base.g * (1 - alpha),
    b: foreground.b * alpha + base.b * (1 - alpha),
    a: 1,
  };
};

const relativeLuminance = ({ r, g, b }: Rgba): number => {
  const channel = (value: number): number => {
    const scaled = value / 255;
    return scaled <= 0.03928 ? scaled / 12.92 : ((scaled + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
};

const contrastRatio = (foreground: Rgba, background: Rgba): number => {
  const solid = relativeLuminance(over(foreground, background));
  const base = relativeLuminance({ ...background, a: 1 });
  const lighter = Math.max(solid, base);
  const darker = Math.min(solid, base);
  return (lighter + 0.05) / (darker + 0.05);
};

interface Surface {
  name: string;
  color: Rgba;
}

const surfaceSet = (declarations: Declarations): Surface[] => {
  const canvas = tokenColor(declarations, '--app-bg');
  const white: Rgba = { r: 255, g: 255, b: 255, a: 1 };
  const isLightTheme = relativeLuminance(canvas) > 0.5;
  const card = over(tokenColor(declarations, '--app-surface'), canvas);
  const surfaces: Surface[] = [
    { name: 'canvas', color: canvas },
    { name: 'card', color: card },
  ];
  // A white surface only exists in the light themes; compositing it in the dark
  // theme would invent a background no component can produce.
  const tintBases = [canvas, card];
  if (isLightTheme) {
    surfaces.push({ name: 'white', color: white });
    tintBases.push(white);
  }

  for (const token of [
    '--app-surface-strong',
    '--app-surface-muted',
    '--surface-subtle',
    '--surface-subtle-hover',
    '--surface-subtle-strong',
  ]) {
    const value = declarations.get(token);
    const color = value ? resolveValue(declarations, value) : null;
    if (color) surfaces.push({ name: token, color: over(color, card) });
  }

  const blue = resolveValue(declarations, 'var(--data-blue-base)');
  const accentSoft = resolveValue(declarations, 'var(--app-accent-soft)');
  for (const base of tintBases) {
    if (accentSoft) surfaces.push({ name: 'accent-soft', color: over(accentSoft, base) });
    if (blue) {
      for (const alpha of [0.08, 0.12]) {
        surfaces.push({ name: `blue-${alpha}`, color: over({ ...blue, a: alpha }, base) });
      }
    }
  }
  return surfaces;
};

const worstContrast = (foreground: Rgba, surfaces: Surface[]): { ratio: number; surface: string } =>
  surfaces.reduce(
    (worst, surface) => {
      const ratio = contrastRatio(foreground, surface.color);
      return ratio < worst.ratio ? { ratio, surface: surface.name } : worst;
    },
    { ratio: Number.POSITIVE_INFINITY, surface: '' }
  );

const themeSource = async (): Promise<string> => stripComments(await readStylesheet(THEMES_URL));

const LIGHT_TEXT_TOKENS = [
  '--app-text-primary',
  '--app-text-regular',
  '--app-text-muted',
  '--primary-text',
  '--success-color',
  '--warning-color',
  '--error-color',
  '--info-text',
  '--data-badge-success-text',
  '--data-badge-warning-text',
  '--data-badge-danger-text',
  '--data-badge-info-text',
  '--data-badge-neutral-text',
];

const DARK_TEXT_TOKENS = [
  '--app-text-primary',
  '--app-text-regular',
  '--app-text-muted',
  '--sidebar-text-color',
  '--primary-text',
  '--success-color',
  '--warning-color',
  '--error-color',
  '--info-text',
  '--data-badge-success-text',
  '--data-badge-warning-text',
  '--data-badge-danger-text',
  '--data-badge-info-text',
  '--data-badge-neutral-text',
];

const THEME_CASES = [
  ['light', "[data-theme='white']"],
  ['dark', "[data-theme='dark']"],
] as const;

describe('theme text tokens hold WCAG AA on their own surfaces', () => {
  it.each([
    ['light', "[data-theme='white']", LIGHT_TEXT_TOKENS],
    ['dark', "[data-theme='dark']", DARK_TEXT_TOKENS],
  ])('%s theme text tokens clear 4.5:1 on every surface', async (theme, selector, tokens) => {
    const declarations = themeDeclarations(await themeSource(), selector);
    const surfaces = surfaceSet(declarations);

    for (const token of tokens) {
      const worst = worstContrast(tokenColor(declarations, token), surfaces);
      expect(
        worst.ratio,
        `${theme} ${token} is ${worst.ratio.toFixed(2)}:1 on ${worst.surface}`
      ).toBeGreaterThanOrEqual(AA_TEXT);
    }
  });

  it.each(THEME_CASES)('%s quaternary tier clears 3:1 (glyphs only)', async (theme, selector) => {
    const declarations = themeDeclarations(await themeSource(), selector);
    const worst = worstContrast(tokenColor(declarations, '--text-quaternary'), surfaceSet(declarations));
    expect(
      worst.ratio,
      `${theme} --text-quaternary is ${worst.ratio.toFixed(2)}:1 on ${worst.surface}`
    ).toBeGreaterThanOrEqual(AA_GLYPH);
  });

  it.each(THEME_CASES)('%s badge text keeps 4.5:1 on its own badge background', async (theme, selector) => {
    const declarations = themeDeclarations(await themeSource(), selector);
    const card = over(
      tokenColor(declarations, '--app-surface'),
      tokenColor(declarations, '--app-bg')
    );

    for (const tone of ['success', 'warning', 'danger', 'info', 'neutral']) {
      const text = tokenColor(declarations, `--data-badge-${tone}-text`);
      const background = over(tokenColor(declarations, `--data-badge-${tone}-bg`), card);
      const ratio = contrastRatio(text, background);
      expect(
        ratio,
        `${theme} --data-badge-${tone}-text on --data-badge-${tone}-bg is ${ratio.toFixed(2)}:1`
      ).toBeGreaterThanOrEqual(AA_TEXT);
    }
  });
});
