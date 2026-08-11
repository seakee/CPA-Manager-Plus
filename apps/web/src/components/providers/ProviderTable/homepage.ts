const SCHEME_RE = /^[a-z][a-z0-9+.-]*:\/\//i;

export const resolveProviderHomepage = (baseUrl: string): string | null => {
  const trimmed = baseUrl.trim();
  if (!trimmed) return null;
  const withScheme = SCHEME_RE.test(trimmed) ? trimmed : `https://${trimmed}`;
  try {
    return new URL(withScheme).origin || null;
  } catch {
    return null;
  }
};
