import { describe, expect, it, vi } from 'vitest';
import { getInspectionConfigFocusTarget } from './inspectionConfigFocus';

describe('getInspectionConfigFocusTarget', () => {
  it('keeps a directly focusable target', () => {
    const target = {
      matches: vi.fn(() => true),
      querySelector: vi.fn(),
    } as unknown as HTMLElement;

    expect(getInspectionConfigFocusTarget(target)).toBe(target);
    expect(target.querySelector).not.toHaveBeenCalled();
  });

  it('finds a focusable control inside a field wrapper', () => {
    const nested = {} as HTMLElement;
    const target = {
      matches: vi.fn(() => false),
      querySelector: vi.fn(() => nested),
    } as unknown as HTMLElement;

    expect(getInspectionConfigFocusTarget(target)).toBe(nested);
  });
});
