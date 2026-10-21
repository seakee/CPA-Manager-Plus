import { describe, expect, it } from 'vitest';
import { getDashboardModelCountDisplay } from './modelCountDisplay';

describe('getDashboardModelCountDisplay', () => {
  it('keeps a successful count during a refresh or failure', () => {
    expect(getDashboardModelCountDisplay(4, true, null)).toBe(4);
    expect(getDashboardModelCountDisplay(4, false, 'offline')).toBe(4);
  });
  it('shows a dash while loading or after failure', () => {
    expect(getDashboardModelCountDisplay(0, true, null)).toBe('-');
    expect(getDashboardModelCountDisplay(0, false, 'failed')).toBe('-');
  });

  it('shows zero for a successful empty response', () => {
    expect(getDashboardModelCountDisplay(0, false, null)).toBe(0);
  });
});
