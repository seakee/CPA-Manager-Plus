/**
 * Redirect mapping for the legacy GitHub Pages origin.
 *
 * `https://seakee.github.io/CPA-Manager-Plus/**` used to serve the embedded demo
 * and the VitePress docs. Both moved out of this repository: the demo is a
 * frozen 1.x deployment at https://cpamp-demo.seakee.dev/ and the docs live at
 * https://cpamp-docs.seakee.dev/. This module is the single source of the
 * mapping; `build.mjs` renders it into the static files in this directory and
 * `tests/pagesRedirect.test.mjs` asserts the mapping table.
 *
 * The published site is static, so every entry point (`index.html`, `404.html`)
 * inlines the same logic. Keep the source below self-contained: no imports, no
 * I/O, and only `location`-shaped data as input, because the generator copies
 * this text verbatim into the page.
 */

export const LEGACY_BASE_PATH = '/CPA-Manager-Plus';
export const DEMO_ORIGIN = 'https://cpamp-demo.seakee.dev';
export const DOCS_ORIGIN = 'https://cpamp-docs.seakee.dev';

export const REDIRECT_SOURCE = `
var LEGACY_BASE_PATH = '${LEGACY_BASE_PATH}';
var DEMO_ORIGIN = '${DEMO_ORIGIN}';
var DOCS_ORIGIN = '${DOCS_ORIGIN}';
var DOCS_PREFIX = LEGACY_BASE_PATH + '/docs';

var resolveRedirect = function (location) {
  var pathname = (location && location.pathname) || '/';
  var search = (location && location.search) || '';
  var hash = (location && location.hash) || '';
  // A static host answers an unknown path with 404.html, which keeps the
  // requested path in \`location\`; GitHub Pages additionally reaches it with the
  // real 404 status. Both resolve the same way.
  if (location && location.href) {
    var target = new URL(location.href);
    if (pathname === '/') pathname = target.pathname;
    if (!search) search = target.search;
    if (!hash) hash = target.hash;
  }
  var suffix = search === '?' ? '' : search;

  if (pathname === DOCS_PREFIX || pathname === DOCS_PREFIX + '/') {
    return { kind: 'docs', url: DOCS_ORIGIN + '/next/' + suffix + hash, status: '301' };
  }
  if (pathname.indexOf(DOCS_PREFIX + '/') === 0) {
    return {
      kind: 'docs',
      url: DOCS_ORIGIN + '/next/' + pathname.slice(DOCS_PREFIX.length + 1) + suffix + hash,
      status: '301',
    };
  }

  // The legacy Pages site had no home page of its own: only the demo was served
  // at the origin root, so the root and every unmatched path land on the demo.
  return { kind: 'demo', url: DEMO_ORIGIN + '/' + suffix + hash, status: '302' };
};
`;

/**
 * Reference implementation of the same mapping, used by the tests and kept in
 * sync with `REDIRECT_SOURCE` by the in-page assertions.
 *
 * @param {object} location `location`-shaped object: `pathname`, `search`, `hash`, `href`
 * @returns {{ kind: 'demo' | 'docs', url: string, status: '301' | '302' }}
 */
export const resolveRedirect = ({ pathname = '/', search = '', hash = '', href = '' } = {}) => {
  if (href) {
    const target = new URL(href);
    if (pathname === '/') pathname = target.pathname;
    if (!search) search = target.search;
    if (!hash) hash = target.hash;
  }
  const suffix = search === '?' ? '' : search;

  if (pathname === `${LEGACY_BASE_PATH}/docs` || pathname === `${LEGACY_BASE_PATH}/docs/`) {
    return { kind: 'docs', url: `${DOCS_ORIGIN}/next/${suffix}${hash}`, status: '301' };
  }
  if (pathname.startsWith(`${LEGACY_BASE_PATH}/docs/`)) {
    const rest = pathname.slice(LEGACY_BASE_PATH.length + '/docs/'.length);
    return { kind: 'docs', url: `${DOCS_ORIGIN}/next/${rest}${suffix}${hash}`, status: '301' };
  }
  return { kind: 'demo', url: `${DEMO_ORIGIN}/${suffix}${hash}`, status: '302' };
};
