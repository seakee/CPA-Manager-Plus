import type { PluginListEntry } from '@/types';
import { normalizeApiBase } from '@/utils/connection';

export const PLUGIN_RESOURCE_PATH_PREFIX = '/v0/resource/plugins/';

export const getPluginTitle = (plugin: Pick<PluginListEntry, 'id' | 'metadata'>): string =>
  plugin.metadata?.name.trim() || plugin.id;

// A plugin page may only be loaded from this deployment's own plugin resource
// namespace. External http(s), data:, blob: and bare relative targets are
// unsupported: the sandbox document is served with an opaque-origin CSP, so
// only a same-origin prefix URL keeps the response under Manager control.
export const isSupportedPluginResourcePath = (value: string): boolean => {
  const trimmed = value.trim();
  if (!trimmed.startsWith(PLUGIN_RESOURCE_PATH_PREFIX)) return false;
  // Reject scheme-relative and backslash aliases, encoded separators, and any
  // dot segment that would escape the namespace after normalization. Percent
  // encoding is refused outright: the Manager's plugin resource predicate only
  // admits paths whose escaped form equals the decoded form, so an encoded
  // segment could never be served and only exists to smuggle a traversal.
  if (/[\\\s%]/.test(trimmed)) return false;
  if (trimmed.includes('//') || trimmed.includes('/./') || trimmed.includes('/../')) return false;
  if (trimmed.endsWith('/..') || trimmed.endsWith('/.')) return false;
  // One plugin id segment must be present.
  const rest = trimmed.slice(PLUGIN_RESOURCE_PATH_PREFIX.length);
  if (!rest) return false;
  // The namespace is served from the origin root, not below the Manager API
  // prefix, so the remainder must stay a plain relative path.
  return !/^[a-z][a-z0-9+.-]*:/i.test(rest) && !rest.startsWith('/');
};

const documentOrigin = (): string => {
  if (typeof window === 'undefined') return '';
  const origin = window.location?.origin;
  return typeof origin === 'string' ? origin : '';
};

// Plugin resources are answered at the origin root by the Manager proxy. The
// console's configured API base therefore contributes only its origin: the
// `/api/v2/cpamp` namespace never serves plugin documents, and a cross-origin
// base cannot satisfy the admin document's `frame-src 'self'` policy or the
// Manager's opaque-origin sandbox response contract.
export const resolvePluginAssetURL = (value: string, apiBase: string): string => {
  const trimmed = value.trim();
  if (!isSupportedPluginResourcePath(trimmed)) return '';

  const origin = documentOrigin();
  if (!origin) return '';
  const rawBase = (apiBase || '').trim();
  if (!rawBase) return `${origin}${trimmed}`;
  if (rawBase.startsWith('//')) return '';
  const base = normalizeApiBase(rawBase);
  if (!base) return `${origin}${trimmed}`;
  try {
    if (new URL(base, origin).origin !== origin) return '';
  } catch {
    return '';
  }
  return `${origin}${trimmed}`;
};
