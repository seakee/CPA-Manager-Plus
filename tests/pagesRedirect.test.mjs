import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import {
  DEMO_ORIGIN,
  DOCS_ORIGIN,
  LEGACY_BASE_PATH,
  REDIRECT_SOURCE,
  resolveRedirect,
} from '../bin/pages-redirect/redirect.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const redirectDir = path.join(root, 'bin/pages-redirect');
const pages = ['index.html', '404.html'];

const location = (pathname, { search = '', hash = '' } = {}) => ({
  pathname,
  search,
  hash,
  href: `https://seakee.github.io${pathname}${search}${hash}`,
});

const target = (pathname, options) =>
  resolveRedirect(location(pathname, options)).url;

describe('legacy Pages redirect mapping', () => {
  it('sends the legacy root to the demo root and preserves the hash', () => {
    expect(target('/CPA-Manager-Plus/')).toBe('https://cpamp-demo.seakee.dev/');
    expect(target('/CPA-Manager-Plus/index.html')).toBe('https://cpamp-demo.seakee.dev/');
    expect(target('/CPA-Manager-Plus/', { hash: '#/demo/accounts' })).toBe(
      'https://cpamp-demo.seakee.dev/#/demo/accounts'
    );
    expect(target('/CPA-Manager-Plus/index.html', { hash: '#/demo/monitoring' })).toBe(
      'https://cpamp-demo.seakee.dev/#/demo/monitoring'
    );
  });

  it('sends every other legacy path to the demo root', () => {
    for (const pathname of [
      '/CPA-Manager-Plus',
      '/CPA-Manager-Plus/demo/accounts',
      '/CPA-Manager-Plus/assets/index-abc123.js',
      '/CPA-Manager-Plus/docsite',
      '/elsewhere',
    ]) {
      expect(resolveRedirect(location(pathname))).toEqual({
        kind: 'demo',
        url: 'https://cpamp-demo.seakee.dev/',
        status: '302',
      });
    }
    expect(target('/CPA-Manager-Plus/docsite', { hash: '#/demo/plugins' })).toBe(
      'https://cpamp-demo.seakee.dev/#/demo/plugins'
    );
  });

  it('sends legacy docs paths to the next docs site and preserves the rest of the path', () => {
    expect(target('/CPA-Manager-Plus/docs/')).toBe('https://cpamp-docs.seakee.dev/next/');
    expect(target('/CPA-Manager-Plus/docs')).toBe('https://cpamp-docs.seakee.dev/next/');
    expect(target('/CPA-Manager-Plus/docs/en/')).toBe('https://cpamp-docs.seakee.dev/next/en/');
    expect(target('/CPA-Manager-Plus/docs/en/guide/getting-started.html')).toBe(
      'https://cpamp-docs.seakee.dev/next/en/guide/getting-started.html'
    );
    expect(target('/CPA-Manager-Plus/docs/guide/choosing-a-panel.html')).toBe(
      'https://cpamp-docs.seakee.dev/next/guide/choosing-a-panel.html'
    );
    expect(resolveRedirect(location('/CPA-Manager-Plus/docs/en/'))).toEqual({
      kind: 'docs',
      url: 'https://cpamp-docs.seakee.dev/next/en/',
      status: '301',
    });
  });

  it('keeps query strings and hashes on every branch', () => {
    expect(
      target('/CPA-Manager-Plus/docs/en/reference/capability-matrix.html', {
        search: '?highlight=quota&page=2',
        hash: '#capability-matrix',
      })
    ).toBe(
      'https://cpamp-docs.seakee.dev/next/en/reference/capability-matrix.html?highlight=quota&page=2#capability-matrix'
    );
    expect(
      target('/CPA-Manager-Plus/docs/', { hash: '#runtime-model' })
    ).toBe('https://cpamp-docs.seakee.dev/next/#runtime-model');
    expect(
      target('/CPA-Manager-Plus/', { search: '?from=readme' })
    ).toBe('https://cpamp-demo.seakee.dev/?from=readme');
    // `location.hash` includes `#`; a relative link keeps the same segment.
    expect(target('/CPA-Manager-Plus/demo', { hash: '#/demo/usage-analytics' })).toBe(
      'https://cpamp-demo.seakee.dev/#/demo/usage-analytics'
    );
  });

  it('resolves from a bare href the way a static host reports it', () => {
    expect(resolveRedirect({ href: 'https://seakee.github.io/CPA-Manager-Plus/docs/en/' }).url).toBe(
      'https://cpamp-docs.seakee.dev/next/en/'
    );
    expect(resolveRedirect({ href: 'https://seakee.github.io/CPA-Manager-Plus/' }).url).toBe(
      'https://cpamp-demo.seakee.dev/'
    );
    // A 404 for an unknown legacy path keeps the requested path in `location`,
    // so the mapping still applies even when the host reports `/404.html`.
    expect(
      resolveRedirect({
        pathname: '/CPA-Manager-Plus/docs/en/manual/accounts.html',
        href: 'https://seakee.github.io/CPA-Manager-Plus/docs/en/manual/accounts.html',
      }).url
    ).toBe('https://cpamp-docs.seakee.dev/next/en/manual/accounts.html');
    expect(
      resolveRedirect({
        pathname: '/CPA-Manager-Plus/docs/en/manual/accounts.html',
        search: '?q=1',
      }).url
    ).toBe('https://cpamp-docs.seakee.dev/next/en/manual/accounts.html?q=1');
  });
});

describe('legacy Pages redirect artifact', () => {
  const runPage = (page, loc) => {
    const html = readFileSync(path.join(redirectDir, page), 'utf8');
    const scripts = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map((match) => match[1]);
    const replacements = [];
    const location = { ...loc, replace: (url) => replacements.push(url) };
    for (const script of scripts) runInNewContext(script, { window: { location }, location, URL });
    return { html, replacements };
  };

  it('publishes only self-contained files and .nojekyll', () => {
    for (const page of pages) {
      const { html } = runPage(page, location('/CPA-Manager-Plus/'));
      expect(html).toContain('<meta name="robots" content="noindex" />');
      expect(html).toContain('<meta http-equiv="refresh"');
      expect(html).toContain('<link rel="canonical"');
      // No external assets: no stylesheet, module, font, or image fetches.
      expect(html).not.toMatch(/<link[^>]+rel="(?:stylesheet|modulepreload|icon)"/u);
      expect(html).not.toMatch(/<(?:img|iframe|source)\b/u);
      expect(html).not.toMatch(/<script[^>]+src=/u);
      expect(html).not.toMatch(/url\(/u);
    }
    expect(readFileSync(path.join(redirectDir, '.nojekyll'), 'utf8')).toBe('');
  });

  it('redirects in the browser with location.replace for the contract paths', () => {
    const cases = [
      ['/CPA-Manager-Plus/', '', '#/demo/accounts', 'https://cpamp-demo.seakee.dev/#/demo/accounts'],
      ['/CPA-Manager-Plus/index.html', '', '', 'https://cpamp-demo.seakee.dev/'],
      [
        '/CPA-Manager-Plus/docs/en/',
        '?q=1',
        '',
        'https://cpamp-docs.seakee.dev/next/en/?q=1',
      ],
      [
        '/CPA-Manager-Plus/docs/guide/getting-started.html',
        '',
        '#install',
        'https://cpamp-docs.seakee.dev/next/guide/getting-started.html#install',
      ],
      ['/CPA-Manager-Plus/other/path', '', '', 'https://cpamp-demo.seakee.dev/'],
    ];
    for (const page of pages) {
      for (const [pathname, search, hash, expected] of cases) {
        const { replacements } = runPage(page, location(pathname, { search, hash }));
        expect(replacements).toEqual([expected]);
      }
    }
  });

  it('keeps the checked-in pages byte-identical to the generator output', async () => {
    const { redirectSiteFiles } = await import('../bin/pages-redirect/build.mjs');
    const expected = redirectSiteFiles();
    expect([...pages, '.nojekyll']).toEqual(Object.keys(expected));
    for (const [name, content] of Object.entries(expected)) {
      expect(readFileSync(path.join(redirectDir, name), 'utf8')).toBe(content);
    }
    expect(LEGACY_BASE_PATH).toBe('/CPA-Manager-Plus');
    expect(DEMO_ORIGIN).toBe('https://cpamp-demo.seakee.dev');
    expect(DOCS_ORIGIN).toBe('https://cpamp-docs.seakee.dev');
  });

  it('keeps the inlined mapping in step with the reference implementation', () => {
    const context = { URL };
    runInNewContext(`${REDIRECT_SOURCE}\nthis.resolveRedirect = resolveRedirect;`, context);
    for (const [pathname, search, hash, href] of [
      ['/CPA-Manager-Plus/', '', '', ''],
      ['/CPA-Manager-Plus/index.html', '', '#/demo/accounts', ''],
      ['/CPA-Manager-Plus/docs', '?q=1', '', ''],
      ['/CPA-Manager-Plus/docs/', '', '#top', ''],
      ['/CPA-Manager-Plus/docs/en/guide/getting-started.html', '?a=1', '#b', ''],
      ['/CPA-Manager-Plus/unknown', '', '#/demo/plugins', ''],
      ['/', '', '', 'https://seakee.github.io/CPA-Manager-Plus/docs/en/'],
      ['/', '', '', 'https://seakee.github.io/CPA-Manager-Plus/'],
    ]) {
      const input = { pathname, search, hash, href };
      expect(context.resolveRedirect(input)).toEqual(resolveRedirect(input));
    }
  });
});
