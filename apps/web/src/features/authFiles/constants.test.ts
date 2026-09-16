import { describe, expect, it } from 'vitest';
import type { AuthFileType } from '@/types';
import { getAuthFileIcon, getTypeColor } from './constants';

describe('authFiles constants - devin', () => {
  it('returns valid distinct light and dark icons for devin', () => {
    const light = getAuthFileIcon('devin', 'light');
    const dark = getAuthFileIcon('devin', 'dark');

    expect(light).toBeTruthy();
    expect(dark).toBeTruthy();
    expect(light).not.toBe(dark);
  });

  it('returns correct type colors for devin in light and dark mode', () => {
    expect(getTypeColor('devin', 'light')).toEqual({
      bg: '#e8f4ff',
      text: '#155e9b',
    });
    expect(getTypeColor('devin', 'dark')).toEqual({
      bg: '#123b5d',
      text: '#8dc9f5',
    });
  });

  it('supports devin as AuthFileType', () => {
    const type: AuthFileType = 'devin';
    expect(type).toBe('devin');
  });
});
