export const normalizeOAuthExcludedRule = (value: string) => value.trim().toLowerCase();

export const addOAuthExcludedRule = (rules: Iterable<string>, value: string) => {
  const normalized = normalizeOAuthExcludedRule(value);
  const next = new Set(Array.from(rules, normalizeOAuthExcludedRule).filter(Boolean));
  if (normalized) next.add(normalized);
  return next;
};

export const serializeOAuthExcludedRules = (rules: Iterable<string>) =>
  Array.from(new Set(Array.from(rules, normalizeOAuthExcludedRule).filter(Boolean))).sort();
