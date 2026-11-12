import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import {
  DOCS_ORIGIN,
  DEMO_ORIGIN,
  LEGACY_BASE_PATH,
  applyRedirect,
  resolveRedirectTarget,
} from '../bin/pages-redirect/redirect.mjs';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const redirectRoot = path.join(repoRoot, 'bin', 'pages-redirect');
const readRedirectAsset = (name) => readFileSync(path.join(redirectRoot, name), 'utf8');

describe('legacy GitHub Pages redirect', () => {
  it('sends the demo root and index.html to the demo site, keeping the hash', () => {
    expect(resolveRedirectTarget('/CPA-Manager-Plus/')).toBe('https://cpamp-demo.seakee.dev/');
    expect(resolveRedirectTarget('/CPA-Manager-Plus/index.html')).toBe(
      'https://cpamp-demo.seakee.dev/'
    );
    expect(resolveRedirectTarget(`/${LEGACY_BASE_PATH.slice(1)}/`, '', '#/demo/accounts')).toBe(
      'https://cpamp-demo.seakee.dev/#/demo/accounts'
    );
    expect(
      resolveRedirectTarget('/CPA-Manager-Plus/index.html', '?from=readme', '#/demo/monitoring')
    ).toBe('https://cpamp-demo.seakee.dev/?from=readme#/demo/monitoring');
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
        `https://cpamp-demo.seakee.dev/${route}`
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

  it('falls back to the demo root for any other legacy path', () => {
    expect(resolveRedirectTarget('/')).toBe('https://cpamp-demo.seakee.dev/');
    expect(resolveRedirectTarget('/CPA-Manager-Plus/unknown/page.html')).toBe(
      'https://cpamp-demo.seakee.dev/'
    );
    expect(resolveRedirectTarget('/other-repo/')).toBe('https://cpamp-demo.seakee.dev/');
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
  });

  it('ships the redirect pages as self-contained static files', () => {
    for (const name of ['index.html', '404.html']) {
      const html = readRedirectAsset(name);
      expect(html, `${name} noindex`).toMatch(/<meta name="robots" content="noindex"\s*\/?>/);
      expect(html, `${name} refresh fallback`).toMatch(
        /<meta http-equiv="refresh" content="0; url=https:\/\/cpamp-demo\.seakee\.dev\/"\s*\/?>/
      );
      expect(html, `${name} canonical`).toContain('<link rel="canonical"');
      expect(html, `${name} bootstrap`).toContain('applyRedirect(window.location');
      expect(html, `${name} no external assets`).not.toMatch(/(src|href)="https?:\/\/(?!cpamp-)/);
    }

    expect(readRedirectAsset('.nojekyll')).toBe('');
  });
});
