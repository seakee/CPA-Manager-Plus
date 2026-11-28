import { describe, expect, it } from 'vitest';

const spanRule =
  /\.quotaWindowGrid\s*>\s*\.quotaWindowCard:nth-child\(3\):last-child\s*,\s*\.quotaWindowGrid\s*>\s*\.quotaWindowCard:nth-child\(5\):last-child\s*\{[^}]*grid-column:\s*1\s*\/\s*-1\s*;/;

describe('quota window grid layout rules', () => {
  it('spans the last card of a three-window or five-window row across both columns', async () => {
    const stylesheetUrl = new URL('./AccountsPage.module.scss', import.meta.url);
    // apps/web/tsconfig.json targets browser globals without @types/node.
    const nodeFsSpecifier = 'node:fs';
    const { readFileSync } = (await import(nodeFsSpecifier)) as {
      readFileSync: (path: URL, encoding: 'utf8') => string;
    };
    const accountsPageStyles = readFileSync(stylesheetUrl, 'utf8');

    expect(accountsPageStyles).toContain(
      '.quotaWindowGrid > .quotaWindowCard:nth-child(3):last-child'
    );
    expect(accountsPageStyles).toContain(
      '.quotaWindowGrid > .quotaWindowCard:nth-child(5):last-child'
    );
    expect(accountsPageStyles).toMatch(spanRule);
    expect(accountsPageStyles).toMatch(/grid-template-columns:\s*1fr\s*;/);
  });

  it('preserves compact btn-xs height and borderless ghost toggle inside bootstrap input-wrap', async () => {
    const carrierCssUrl = new URL('../../fullweb/carrier.css', import.meta.url);
    // apps/web/tsconfig.json targets browser globals without @types/node.
    const nodeFsSpecifier = 'node:fs';
    const { readFileSync } = (await import(nodeFsSpecifier)) as {
      readFileSync: (path: URL, encoding: 'utf8') => string;
    };
    const carrierCss = readFileSync(carrierCssUrl, 'utf8');

    expect(carrierCss).toMatch(
      /\.bootstrap-main\s+\.btn\.btn-xs\s*\{[^}]*height:\s*var\(--control-height-xs\);[^}]*min-height:\s*var\(--control-height-xs\);/
    );
    expect(carrierCss).toMatch(
      /\.input-wrap\s+\.input-actions\s+\.btn-ghost\s*\{[^}]*border-color:\s*transparent;/
    );
  });
});
