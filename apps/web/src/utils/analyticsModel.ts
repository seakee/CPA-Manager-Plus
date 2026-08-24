const REASONING_MODEL_SUFFIXES = new Set([
  'none',
  'auto',
  '-1',
  'minimal',
  'low',
  'medium',
  'high',
  'xhigh',
  'max',
]);

const isReasoningModelSuffix = (value: string): boolean => {
  if (REASONING_MODEL_SUFFIXES.has(value.toLowerCase())) return true;
  if (!/^[+-]?\d+$/.test(value)) return false;
  try {
    return BigInt(value) >= 0n && BigInt(value) <= 9_223_372_036_854_775_807n;
  } catch {
    return false;
  }
};

/**
 * Returns the canonical model identity used by usage aggregation and pricing.
 * Only reasoning-configuration suffixes recognized by CPA are removed.
 */
export const normalizeAnalyticsModel = (value: unknown): string => {
  const model = value === null || value === undefined ? '' : String(value);
  const open = model.lastIndexOf('(');
  if (open <= 0 || !model.endsWith(')')) return model;
  const suffix = model.slice(open + 1, -1);
  if (!isReasoningModelSuffix(suffix)) return model;
  return model.slice(0, open) || model;
};
