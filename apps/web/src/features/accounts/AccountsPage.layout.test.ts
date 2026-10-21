import { describe, expect, it } from 'vitest';

const spanRule =
  /\.quotaWindowGrid\s*>\s*\.quotaWindowCard:nth-child\(3\):last-child\s*,\s*\.quotaWindowGrid\s*>\s*\.quotaWindowCard:nth-child\(5\):last-child\s*\{[^}]*grid-column:\s*1\s*\/\s*-1\s*;/;

describe('quota window grid layout rules', () => {
  it('spans the last card of a three-window or five-window row across both columns', async () => {
    const stylesheetUrl = new URL('./AccountsPage.module.scss', import.meta.url);
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
});
