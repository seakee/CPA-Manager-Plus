import type { ResolvedTheme } from '@/types';

export type DataPalette = {
  blue: string;
  emerald: string;
  green: string;
  amber: string;
  red: string;
  violet: string;
  cyan: string;
  teal: string;
  slate: string;
  slateMuted: string;
  healthWarn: string;
  surface: {
    axisLabel: string;
    axisLine: string;
    axisPointer: string;
    splitLine: string;
    tooltipBackground: string;
    tooltipBorder: string;
    tooltipMuted: string;
    tooltipShadow: string;
    tooltipText: string;
  };
};

export const lightDataPalette: DataPalette = {
  blue: '#3b82f6',
  emerald: '#10b981',
  green: '#22c55e',
  amber: '#f59e0b',
  red: '#ef4444',
  violet: '#8b5cf6',
  cyan: '#06b6d4',
  teal: '#14b8a6',
  slate: '#64748b',
  slateMuted: '#94a3b8',
  healthWarn: '#a3e635',
  surface: {
    axisLabel: '#52606f',
    axisLine: '#e2e8f0',
    axisPointer: '#767f8f',
    splitLine: '#e8edf5',
    tooltipBackground: 'rgba(255,255,255,0.96)',
    tooltipBorder: '#dbe3ef',
    tooltipMuted: '#52606f',
    tooltipShadow: 'box-shadow: 0 16px 36px rgba(15,23,42,0.14);',
    tooltipText: '#0f172a',
  },
};

export const darkDataPalette: DataPalette = {
  blue: '#60a5fa',
  emerald: '#34d399',
  green: '#4ade80',
  amber: '#fbbf24',
  red: '#f87171',
  violet: '#a78bfa',
  cyan: '#22d3ee',
  teal: '#2dd4bf',
  slate: '#94a3b8',
  slateMuted: '#64748b',
  healthWarn: '#bef264',
  surface: {
    axisLabel: '#bcbcbc',
    axisLine: 'rgba(255,255,255,0.12)',
    axisPointer: '#7a7a7a',
    splitLine: 'rgba(255,255,255,0.1)',
    tooltipBackground: 'rgba(24,28,40,0.96)',
    tooltipBorder: 'rgba(255,255,255,0.12)',
    tooltipMuted: '#bcbcbc',
    tooltipShadow: 'box-shadow: 0 16px 36px rgba(0,0,0,0.38);',
    tooltipText: '#e5e5e5',
  },
};

export const getDataPalette = (resolvedTheme: ResolvedTheme): DataPalette =>
  resolvedTheme === 'dark' ? darkDataPalette : lightDataPalette;

export const hexToRgba = (color: string, alpha: number): string => {
  const match = color.match(/^#([\da-f]{2})([\da-f]{2})([\da-f]{2})$/i);
  if (!match) return color;
  const [, r, g, b] = match;
  return `rgba(${Number.parseInt(r, 16)},${Number.parseInt(g, 16)},${Number.parseInt(b, 16)},${alpha})`;
};
