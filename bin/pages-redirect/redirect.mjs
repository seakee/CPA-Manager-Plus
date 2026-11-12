// Pure redirect mapping for the legacy `seakee.github.io/CPA-Manager-Plus/**`
// GitHub Pages site.
//
// This file is the single source of truth. `index.html` and `404.html` cannot
// import it: GitHub Pages serves `404.html` at the *original* nested URL, so a
// relative `./redirect.mjs` request would resolve under `/docs/en/guide/` and
// 404. The page fallback therefore inlines the same logic as a classic script,
// and `tests/pagesRedirect.test.mjs` fails if the inlined copy drifts from this
// module. Keep the body of `legacyRedirectTarget` free of module syntax so the
// executable source can be embedded verbatim.
//
// Mapping:
//   /CPA-Manager-Plus/                    -> https://cpamp-demo.seakee.dev/ + hash
//   /CPA-Manager-Plus/index.html          -> https://cpamp-demo.seakee.dev/ + hash
//   /CPA-Manager-Plus/docs/<rest>         -> https://cpamp-docs.seakee.dev/<rest>
//   any other path                        -> https://cpamp-demo.seakee.dev/
//
// Query strings and hashes are preserved on every target.

import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

export const DEMO_ORIGIN = 'https://cpamp-demo.seakee.dev';
export const DOCS_ORIGIN = 'https://cpamp-docs.seakee.dev';
export const LEGACY_BASE_PATH = '/CPA-Manager-Plus';

// BEGIN INLINE
function legacyRedirectTarget(pathname, search, hash) {
  var DEMO = 'https://cpamp-demo.seakee.dev';
  var DOCS = 'https://cpamp-docs.seakee.dev';
  var BASE = '/CPA-Manager-Plus';
  var suffix = (search || '') + (hash || '');
  var normalized = (pathname || '/').replace(/[\\/]+/g, '/');
  var rest;

  if (normalized.indexOf(BASE + '/') === 0) {
    rest = normalized.slice(BASE.length);
  } else if (normalized === BASE) {
    rest = '/';
  } else {
    rest = normalized;
  }

  if (rest === '/' || rest === '/index.html') {
    return DEMO + '/' + suffix;
  }

  if (rest.indexOf('/docs/') === 0) {
    return DOCS + '/' + rest.slice('/docs/'.length) + suffix;
  }

  // `/docs` without a trailing slash is the docs root.
  if (rest === '/docs') {
    return DOCS + '/' + suffix;
  }

  return DEMO + '/' + suffix;
}
// END INLINE

/**
 * Resolve the new-site URL for a legacy GitHub Pages request.
 *
 * @param {string} pathname Request path, e.g. `/CPA-Manager-Plus/docs/en/`.
 * @param {string} [search] Query string including `?` when present.
 * @param {string} [hash] Fragment including `#` when present.
 * @returns {string} Absolute URL on the new site.
 */
export const resolveRedirectTarget = (pathname, search = '', hash = '') =>
  legacyRedirectTarget(pathname, search, hash);

/**
 * Redirect the current browser location.
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

/**
 * The executable payload embedded in `index.html` and `404.html`. Derived from
 * this file's own text between the `BEGIN INLINE`/`END INLINE` markers so the
 * shipped HTML is byte-identical to the source above. Exported so the drift
 * test can assert both pages contain it exactly.
 */
const MODULE_SOURCE = readFileSync(fileURLToPath(import.meta.url), 'utf8');
const INLINE_BLOCK = MODULE_SOURCE.slice(
  MODULE_SOURCE.indexOf('// BEGIN INLINE'),
  MODULE_SOURCE.indexOf('// END INLINE') + '// END INLINE'.length
);

/** Full script body inlined in both HTML files, function plus bootstrap. */
export const INLINE_SCRIPT = `${INLINE_BLOCK}
(function () {
  var target = legacyRedirectTarget(
    window.location.pathname,
    window.location.search,
    window.location.hash
  );
  window.location.replace(target);
})();`;
