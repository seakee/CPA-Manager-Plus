import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';
import { describe, expect, it } from 'vitest';
import {
  DEMO_ORIGIN,
  DOCS_ORIGIN,
  INLINE_SCRIPT,
  LEGACY_BASE_PATH,
  applyRedirect,
  resolveRedirectTarget,
} from '../bin/pages-redirect/redirect.mjs';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const redirectRoot = path.join(repoRoot, 'bin', 'pages-redirect');
const readRedirectAsset = (name) => readFileSync(path.join(redirectRoot, name), 'utf8');

const PAGES = ['index.html', '404.html'];

/**
 * Extract the inline classic script from a redirect page. The pages must not
 * load an external module: GitHub Pages serves `404.html` at the original
 * nested URL, so `./redirect.mjs` would resolve under `/docs/en/guide/` and
 * 404, leaving only the meta fallback.
 */
const inlineScript = (html) => {
  const match = html.match(/<script>([\s\S]*?)<\/script>/);
  if (!match) throw new Error('redirect page has no inline script');
  // The generator pads the script element; compare the executable body only.
  return match[1].trim();
};

/** Run a page's inline script with a fake `location`, capturing `replace`. */
const runInlineScript = (html, requestPath) => {
  const calls = [];
  const url = new URL(`https://seakee.github.io${requestPath}`);
  const context = vm.createContext({
    window: {
      location: {
        pathname: url.pathname,
        search: url.search,
        hash: url.hash,
        replace: (target) => calls.push(target),
      },
    },
  });

  vm.runInContext(inlineScript(html), context);
  return calls;
};

describe('legacy GitHub Pages redirect', () => {
  it('sends the demo root and index.html to the demo site, keeping the hash', () => {
    expect(resolveRedirectTarget('/CPA-Manager-Plus/')).toBe('https://cpamp-demo.seakee.dev/');
    expect(resolveRedirectTarget('/CPA-Manager-Plus/index.html')).toBe(
      'https://cpamp-demo.seakee.dev/'
    );
    expect(resolveRedirectTarget('/CPA-Manager-Plus/', '', '#/demo/accounts')).toBe(
      'https://cpamp-demo.seakee.dev/#/demo/accounts'
    );
    expect(
      resolveRedirectTarget('/CPA-Manager-Plus/index.html', '?from=readme', '#/demo/monitoring')
    ).toBe('https://cpamp-demo.seakee.dev/?from=readme#/demo/monitoring');
  });

  it('treats the base path without a trailing slash as the demo root', () => {
    expect(resolveRedirectTarget('/CPA-Manager-Plus')).toBe('https://cpamp-demo.seakee.dev/');
    expect(resolveRedirectTarget('/CPA-Manager-Plus', '', '#/demo/accounts')).toBe(
      'https://cpamp-demo.seakee.dev/#/demo/accounts'
    );
  });

  it('keeps every legacy demo hash route on the demo site', () => {
    for (const route of [
      '#/demo',
      '#/demo/accounts',
      '#/demo/monitoring',
      '#/demo/monitoring/account-actions',
      '#/demo/usage-analytics',
      '#/demo/ai-providers',
      '#/demo/codex-inspection',
      '#/demo/model-prices',
      '#/demo/plugins',
    ]) {
      expect(resolveRedirectTarget('/CPA-Manager-Plus/', '', route)).toBe(
        `${DEMO_ORIGIN}/${route}`
      );
    }
  });

  it('maps docs paths to the docs site while preserving en/, .html, query, and hash', () => {
    expect(resolveRedirectTarget('/CPA-Manager-Plus/docs/')).toBe('https://cpamp-docs.seakee.dev/');
    expect(resolveRedirectTarget('/CPA-Manager-Plus/docs/en/')).toBe(
      'https://cpamp-docs.seakee.dev/en/'
    );
    expect(resolveRedirectTarget('/CPA-Manager-Plus/docs/en/guide/getting-started.html')).toBe(
      'https://cpamp-docs.seakee.dev/en/guide/getting-started.html'
    );
    expect(resolveRedirectTarget('/CPA-Manager-Plus/docs/operations/reset-admin-key.html')).toBe(
      'https://cpamp-docs.seakee.dev/operations/reset-admin-key.html'
    );
    expect(
      resolveRedirectTarget(
        '/CPA-Manager-Plus/docs/guide/choosing-a-panel.html',
        '?lang=zh-CN',
        '#对比'
      )
    ).toBe('https://cpamp-docs.seakee.dev/guide/choosing-a-panel.html?lang=zh-CN#对比');
  });

  it('treats /docs without a trailing slash as the docs root', () => {
    expect(resolveRedirectTarget('/CPA-Manager-Plus/docs')).toBe('https://cpamp-docs.seakee.dev/');
  });

  it('collapses repeated slashes and normalizes backslashes', () => {
    expect(resolveRedirectTarget('/CPA-Manager-Plus//')).toBe('https://cpamp-demo.seakee.dev/');
    expect(resolveRedirectTarget('//CPA-Manager-Plus///docs//en//')).toBe(
      'https://cpamp-docs.seakee.dev/en/'
    );
    expect(resolveRedirectTarget('\\CPA-Manager-Plus\\docs\\en\\guide\\start.html')).toBe(
      'https://cpamp-docs.seakee.dev/en/guide/start.html'
    );
    expect(resolveRedirectTarget('/CPA-Manager-Plus\\docs\\en')).toBe(
      'https://cpamp-docs.seakee.dev/en'
    );
  });

  it('keeps percent-encoded characters intact', () => {
    expect(
      resolveRedirectTarget('/CPA-Manager-Plus/docs/%E4%B8%AD%E6%96%87/page.html', '?q=a%20b')
    ).toBe('https://cpamp-docs.seakee.dev/%E4%B8%AD%E6%96%87/page.html?q=a%20b');
    expect(resolveRedirectTarget('/CPA-Manager-Plus/', '', '#/demo/%E8%B4%A6%E5%8F%B7')).toBe(
      'https://cpamp-demo.seakee.dev/#/demo/%E8%B4%A6%E5%8F%B7'
    );
  });

  it('falls back to the demo root for any other legacy path', () => {
    expect(resolveRedirectTarget('/')).toBe('https://cpamp-demo.seakee.dev/');
    expect(resolveRedirectTarget('/CPA-Manager-Plus/unknown/page.html')).toBe(
      'https://cpamp-demo.seakee.dev/'
    );
    expect(resolveRedirectTarget('/other-repo/')).toBe('https://cpamp-demo.seakee.dev/');
  });

  it('never leaves the demo or docs origin, even for hostile inputs', () => {
    const hostile = [
      '//evil.com',
      '//evil.com/CPA-Manager-Plus/docs/',
      '/\\evil.com',
      '\\\\evil.com',
      '/@evil.com',
      '/CPA-Manager-Plus//@evil.com',
      '/CPA-Manager-Plus/docs//evil.com',
      '/CPA-Manager-Plus/docs/../../evil.com',
      'https://evil.com/CPA-Manager-Plus/',
      'evil.com/CPA-Manager-Plus/docs/',
      '',
    ];

    for (const candidate of hostile) {
      for (const suffix of [
        ['', ''],
        ['?x=1', '#y'],
      ]) {
        const target = resolveRedirectTarget(candidate, ...suffix);
        expect([DEMO_ORIGIN, DOCS_ORIGIN], `${candidate} -> ${target}`).toContain(
          new URL(target).origin
        );
      }
    }
  });

  it('replaces rather than pushes history and returns the target', () => {
    const replaced = [];
    const target = applyRedirect(
      { pathname: '/CPA-Manager-Plus/docs/en/', search: '', hash: '#install' },
      (url) => replaced.push(url)
    );

    expect(replaced).toEqual(['https://cpamp-docs.seakee.dev/en/#install']);
    expect(target).toBe('https://cpamp-docs.seakee.dev/en/#install');
  });

  it('exposes the documented origins', () => {
    expect(DEMO_ORIGIN).toBe('https://cpamp-demo.seakee.dev');
    expect(DOCS_ORIGIN).toBe('https://cpamp-docs.seakee.dev');
    expect(LEGACY_BASE_PATH).toBe('/CPA-Manager-Plus');
  });

  it('inlines identical, module-free redirect logic in every page', () => {
    for (const name of PAGES) {
      const html = readRedirectAsset(name);
      const script = inlineScript(html);

      expect(html, `${name} must not load an external module`).not.toMatch(
        /<script[^>]*\b(src|type=["']module["'])/i
      );
      expect(script, `${name} inline drift`).toContain(INLINE_SCRIPT);
      // `import`/`export`/`?`-style module syntax must never reach a page.
      expect(script).not.toMatch(/^\s*(import|export)\s/m);
    }
  });

  it('executes the inline script from every page for nested legacy URLs', () => {
    const samples = [
      '/CPA-Manager-Plus/docs/en/guide/getting-started.html?x=1#y',
      '/CPA-Manager-Plus/docs/',
      '/CPA-Manager-Plus/docs/en/',
      '/CPA-Manager-Plus/',
      '/CPA-Manager-Plus',
      '/CPA-Manager-Plus/#/demo/accounts',
      '/CPA-Manager-Plus/unknown/page.html',
    ];

    for (const name of PAGES) {
      const html = readRedirectAsset(name);
      for (const sample of samples) {
        const calls = runInlineScript(html, sample);
        expect(calls, `${name} ${sample}`).toEqual([
          resolveRedirectTarget(...splitRequest(sample)),
        ]);
      }
    }
  });

  it('keeps the meta refresh as a JS-disabled fallback only', () => {
    for (const name of PAGES) {
      const html = readRedirectAsset(name);

      expect(html, `${name} noindex`).toMatch(/<meta name="robots" content="noindex"\s*\/?>/);
      expect(html, `${name} canonical`).toContain('<link rel="canonical"');
      expect(html, `${name} no external assets`).not.toMatch(/(src|href)="https?:\/\/(?!cpamp-)/);

      // A script error must not silently send docs users to the demo site, so
      // the refresh lives inside <noscript> and never runs alongside the script.
      const noscript = html.match(/<noscript>([\s\S]*?)<\/noscript>/);
      expect(noscript, `${name} noscript fallback`).toBeTruthy();
      expect(noscript[1]).toMatch(
        /<meta http-equiv="refresh" content="0; url=https:\/\/cpamp-demo\.seakee\.dev\/"\s*\/?>/
      );
      expect(html.replace(noscript[0], '')).not.toContain('http-equiv="refresh"');
    }

    expect(readRedirectAsset('.nojekyll')).toBe('');
  });
});

/** Split a legacy request string into `[pathname, search, hash]`. */
function splitRequest(requestPath) {
  const url = new URL(`https://seakee.github.io${requestPath}`);
  return [url.pathname, url.search, url.hash];
}
