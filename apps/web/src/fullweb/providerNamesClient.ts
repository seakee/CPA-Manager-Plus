export interface Group {
  provider: string;
  index: number;
  name: string;
}
export interface View {
  generation: string;
  etag: string;
  writable: boolean;
  groups: Group[];
}

export function parseProviderNames(value: unknown): View {
  const v = value as View;
  if (
    !v ||
    typeof v.generation !== 'string' ||
    typeof v.etag !== 'string' ||
    typeof v.writable !== 'boolean' ||
    !Array.isArray(v.groups) ||
    !v.groups.every(
      (g) =>
        g &&
        typeof g.provider === 'string' &&
        Number.isSafeInteger(g.index) &&
        g.index >= 0 &&
        typeof g.name === 'string'
    )
  )
    throw new Error('invalid provider names');
  return v;
}
