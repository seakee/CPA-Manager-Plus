const focusableSelector =
  'input:not([disabled]), button:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

export const getInspectionConfigFocusTarget = (target: HTMLElement): HTMLElement | null =>
  target.matches(focusableSelector) ? target : target.querySelector<HTMLElement>(focusableSelector);
