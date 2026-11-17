import type { ResolvedTheme } from '@/types';

export type DataPalette = {
  blue: string;
  blueSoft: string;
  emerald: string;
  green: string;
  amber: string;
  red: string;
  violet: string;
  pink: string;
  cyan: string;
  teal: string;
  slate: string;
  slateMuted: string;
  healthWarn: string;
  heatmap: [string, string, string, string];
  surface: {
    axisLabel: string;
    axisLine: string;
    axisPointer: string;
    barBackground: string;
    cellBorder: string;
    emphasisBorder: string;
    pieShadow: string;
    splitLine: string;
    tooltipBackground: string;
    tooltipBorder: string;
    tooltipMuted: string;
    tooltipShadow: string;
    tooltipText: string;
    usageAxisLine: string;
    usageSplitLine: string;
    usageTooltipBorder: string;
    usageTooltipText: string;
  };
};

export const lightDataPalette: DataPalette = {
  blue: '#3b82f6',
  blueSoft: '#60a5fa',
  emerald: '#10b981',
  green: '#22c55e',
  amber: '#f59e0b',
  red: '#ef4444',
  violet: '#8b5cf6',
  pink: '#ec4899',
  cyan: '#06b6d4',
  teal: '#14b8a6',
  slate: '#64748b',
  slateMuted: '#94a3b8',
  healthWarn: '#a3e635',
  heatmap: ['#eff6ff', '#93c5fd', '#3b82f6', '#0f766e'],
  surface: {
    axisLabel: '#8b95a6',
    axisLine: '#e2e8f0',
    axisPointer: '#767f8f',
    barBackground: 'rgba(139, 149, 166, 0.14)',
    cellBorder: '#ffffff',
    emphasisBorder: '#2c3e50',
    pieShadow: 'rgba(15, 23, 42, 0.18)',
    splitLine: '#e8edf5',
    tooltipBackground: 'rgba(255,255,255,0.96)',
    tooltipBorder: '#dbe3ef',
    tooltipMuted: '#8b95a6',
    tooltipShadow: 'box-shadow: 0 16px 36px rgba(15,23,42,0.14);',
    tooltipText: '#0f172a',
    usageAxisLine: '#d8e5f2',
    usageSplitLine: '#d3e1ef',
    usageTooltipBorder: '#d8e5f2',
    usageTooltipText: '#2c3e50',
  },
};

export const darkDataPalette: DataPalette = {
  blue: '#60a5fa',
  blueSoft: '#60a5fa',
  emerald: '#34d399',
  green: '#4ade80',
  amber: '#fbbf24',
  red: '#f87171',
  violet: '#a78bfa',
  pink: '#f472b6',
  cyan: '#22d3ee',
  teal: '#2dd4bf',
  slate: '#94a3b8',
  slateMuted: '#64748b',
  healthWarn: '#bef264',
  heatmap: ['#102f4f', '#1d5f98', '#3b82f6', '#60a5fa'],
  surface: {
    axisLabel: '#bcbcbc',
    axisLine: 'rgba(255,255,255,0.12)',
    axisPointer: '#7a7a7a',
    barBackground: 'rgba(255, 255, 255, 0.08)',
    cellBorder: '#1b1f2a',
    emphasisBorder: '#e5e5e5',
    pieShadow: 'rgba(0, 0, 0, 0.36)',
    splitLine: 'rgba(255,255,255,0.1)',
    tooltipBackground: 'rgba(24,28,40,0.96)',
    tooltipBorder: 'rgba(255,255,255,0.12)',
    tooltipMuted: '#bcbcbc',
    tooltipShadow: 'box-shadow: 0 16px 36px rgba(0,0,0,0.38);',
    tooltipText: '#e5e5e5',
    usageAxisLine: 'rgba(255,255,255,0.12)',
    usageSplitLine: 'rgba(255,255,255,0.1)',
    usageTooltipBorder: 'rgba(255,255,255,0.12)',
    usageTooltipText: '#e5e5e5',
  },
};

const getActiveDomTheme = (): ResolvedTheme | null => {
  if (typeof window === 'undefined' || typeof document === 'undefined' || !document.documentElement) {
    return null;
  }
  return document.documentElement.getAttribute('data-theme') === 'dark' ? 'dark' : 'light';
};

export const readThemeToken = (
  tokenName: string,
  fallback: string,
  expectedTheme?: ResolvedTheme
): string => {
  const domTheme = getActiveDomTheme();
  if (!domTheme || (expectedTheme && domTheme !== expectedTheme)) {
    return fallback;
  }
  const value = window.getComputedStyle(document.documentElement).getPropertyValue(tokenName).trim();
  return value || fallback;
};

export const getDataPalette = (resolvedTheme: ResolvedTheme): DataPalette => {
  const base = resolvedTheme === 'dark' ? darkDataPalette : lightDataPalette;
  if (getActiveDomTheme() !== resolvedTheme) {
    return base;
  }
  const r = (tokenName: string, fallback: string) =>
    readThemeToken(tokenName, fallback, resolvedTheme);
  const tooltipShadow = r('--chart-tooltip-shadow', '');

  return {
    blue: r('--data-blue-base', base.blue),
    blueSoft: r('--data-blue-light-3', base.blueSoft),
    emerald: r('--data-emerald-base', base.emerald),
    green: r('--data-green-base', base.green),
    amber: r('--data-amber-base', base.amber),
    red: r('--data-red-base', base.red),
    violet: r('--data-violet-base', base.violet),
    pink: r('--data-pink-base', base.pink),
    cyan: r('--data-cyan-base', base.cyan),
    teal: r('--data-teal-base', base.teal),
    slate: r('--data-slate-base', base.slate),
    slateMuted: r('--data-slate-light-3', base.slateMuted),
    healthWarn: r('--data-health-warn-base', base.healthWarn),
    heatmap: [
      r('--chart-heatmap-1', base.heatmap[0]),
      r('--chart-heatmap-2', base.heatmap[1]),
      r('--chart-heatmap-3', base.heatmap[2]),
      r('--chart-heatmap-4', base.heatmap[3]),
    ],
    surface: {
      axisLabel: r('--chart-axis-label', base.surface.axisLabel),
      axisLine: r('--chart-axis-line', base.surface.axisLine),
      axisPointer: r('--chart-axis-pointer', base.surface.axisPointer),
      barBackground: r('--chart-bar-bg', base.surface.barBackground),
      cellBorder: r('--chart-cell-border', base.surface.cellBorder),
      emphasisBorder: r('--chart-emphasis-border', base.surface.emphasisBorder),
      pieShadow: r('--chart-pie-shadow', base.surface.pieShadow),
      splitLine: r('--chart-split-line', base.surface.splitLine),
      tooltipBackground: r('--chart-tooltip-bg', base.surface.tooltipBackground),
      tooltipBorder: r('--chart-tooltip-border', base.surface.tooltipBorder),
      tooltipMuted: r('--chart-tooltip-muted', base.surface.tooltipMuted),
      tooltipShadow: tooltipShadow ? `box-shadow: ${tooltipShadow};` : base.surface.tooltipShadow,
      tooltipText: r('--chart-tooltip-text', base.surface.tooltipText),
      usageAxisLine: r('--chart-axis-line-usage', base.surface.usageAxisLine),
      usageSplitLine: r('--chart-split-line-usage', base.surface.usageSplitLine),
      usageTooltipBorder: r('--chart-tooltip-border-usage', base.surface.usageTooltipBorder),
      usageTooltipText: r('--chart-tooltip-text-usage', base.surface.usageTooltipText),
    },
  };
};

export const hexToRgba = (color: string, alpha: number): string => {
  const trimmed = color.trim();
  const hex6 = trimmed.match(/^#([\da-f]{2})([\da-f]{2})([\da-f]{2})$/i);
  if (hex6) {
    const [, r, g, b] = hex6;
    return `rgba(${Number.parseInt(r, 16)},${Number.parseInt(g, 16)},${Number.parseInt(b, 16)},${alpha})`;
  }
  const hex3 = trimmed.match(/^#([\da-f])([\da-f])([\da-f])$/i);
  if (hex3) {
    const [, r, g, b] = hex3;
    return `rgba(${Number.parseInt(r + r, 16)},${Number.parseInt(g + g, 16)},${Number.parseInt(b + b, 16)},${alpha})`;
  }
  const rgb = trimmed.match(/^rgba?\(\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})/i);
  if (rgb) {
    return `rgba(${rgb[1]},${rgb[2]},${rgb[3]},${alpha})`;
  }
  return color;
};
