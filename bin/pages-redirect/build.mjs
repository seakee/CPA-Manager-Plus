/**
 * Renders the static legacy-origin redirect site from `redirect.mjs`.
 *
 * Usage: node bin/pages-redirect/build.mjs
 * Writes `index.html`, `404.html` and `.nojekyll` next to this file. The output
 * is committed; `tests/pagesRedirect.test.mjs` fails when it is stale.
 */
import { readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { REDIRECT_SOURCE } from './redirect.mjs';

const root = path.dirname(fileURLToPath(import.meta.url));

// Every page inlines the same mapping source; the legacy site is static, so the
// browser has no module to import.
const mappingSource = REDIRECT_SOURCE.trim();

const page = (status) => `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <meta name="robots" content="noindex" />
    <title>CPA Manager Plus moved</title>
    <script>
      (function () {
        ${mappingSource.replaceAll('\n', '\n        ')}
        var target = resolveRedirect(window.location);
        // meta refresh and canonical stay in place for non-scripting clients.
        location.replace(target.url);
      })();
    </script>
    <noscript><meta http-equiv="refresh" content="0; url=https://cpamp-demo.seakee.dev/" /></noscript>
    <link rel="canonical" href="https://cpamp-demo.seakee.dev/" />
  </head>
  <body>
    <p data-status="${status}">
      CPA Manager Plus moved. The demo is at
      <a href="https://cpamp-demo.seakee.dev/">cpamp-demo.seakee.dev</a> and the
      documentation is at
      <a href="https://cpamp-docs.seakee.dev/next/">cpamp-docs.seakee.dev</a>.
    </p>
  </body>
</html>
`;

export const renderPage = page;

export const redirectSiteFiles = () => ({
  'index.html': page('200'),
  '404.html': page('404'),
  '.nojekyll': '',
});

export const writeRedirectSite = ({ directory = root } = {}) => {
  const written = [];
  for (const [name, content] of Object.entries(redirectSiteFiles())) {
    const absolutePath = path.join(directory, name);
    try {
      if (readFileSync(absolutePath, 'utf8') === content) continue;
    } catch {
      // Missing file writes below.
    }
    writeFileSync(absolutePath, content);
    written.push(name);
  }
  return written;
};

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const written = writeRedirectSite();
  process.stdout.write(
    written.length === 0
      ? 'pages-redirect: up to date\n'
      : `pages-redirect: wrote ${written.join(', ')}\n`
  );
}
