// Pure redirect mapping for the legacy `seakee.github.io/CPA-Manager-Plus/**`
// GitHub Pages site. Kept dependency-free so both the browser pages
// (`index.html`, `404.html`) and `tests/pagesRedirect.test.mjs` share one
// implementation.
//
// Mapping:
//   /CPA-Manager-Plus/                    -> https://cpamp-demo.seakee.dev/ + hash
//   /CPA-Manager-Plus/index.html          -> https://cpamp-demo.seakee.dev/ + hash
//   /CPA-Manager-Plus/docs/<rest>         -> https://cpamp-docs.seakee.dev/<rest>
//   any other path                        -> https://cpamp-demo.seakee.dev/
//
// Query strings and hashes are preserved on every target.

export const DEMO_ORIGIN = 'https://cpamp-demo.seakee.dev';
export const DOCS_ORIGIN = 'https://cpamp-docs.seakee.dev';
export const LEGACY_BASE_PATH = '/CPA-Manager-Plus';

/**
 * Resolve the new-site URL for a legacy GitHub Pages request.
 *
 * @param {string} pathname Request path, e.g. `/CPA-Manager-Plus/docs/en/`.
 * @param {string} [search] Query string including `?` when present.
 * @param {string} [hash] Fragment including `#` when present.
 * @returns {string} Absolute URL on the new site.
 */
export const resolveRedirectTarget = (pathname, search = '', hash = '') => {
  const suffix = `${search}${hash}`;
  const normalized = (pathname || '/').replace(/\/{2,}/g, '/');
  const withoutBase = normalized.startsWith(`${LEGACY_BASE_PATH}/`)
    ? normalized.slice(LEGACY_BASE_PATH.length)
    : normalized === LEGACY_BASE_PATH
      ? '/'
      : normalized;

  if (withoutBase === '/' || withoutBase === '/index.html') {
    return `${DEMO_ORIGIN}/${suffix}`;
  }

  if (withoutBase.startsWith('/docs/')) {
    return `${DOCS_ORIGIN}/${withoutBase.slice('/docs/'.length)}${suffix}`;
  }

  // `/docs` without a trailing slash is the docs root.
  if (withoutBase === '/docs') {
    return `${DOCS_ORIGIN}/${suffix}`;
  }

  return `${DEMO_ORIGIN}/${suffix}`;
};

/**
 * Redirect the current browser location. Split out so the page bootstrap stays
 * a one-liner and remains testable through `resolveRedirectTarget`.
 *
 * @param {{ pathname: string, search?: string, hash?: string }} location
 * @param {(target: string) => void} [replace]
 * @returns {string} The resolved target URL.
 */
export const applyRedirect = (location, replace) => {
  const target = resolveRedirectTarget(location.pathname, location.search, location.hash);
  if (typeof replace === 'function') replace(target);
  return target;
};
