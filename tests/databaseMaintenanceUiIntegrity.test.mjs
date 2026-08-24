import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const bannerStylesPath = path.join(
  repoRoot,
  'apps/web/src/components/common/DatabaseMaintenanceBanner.module.scss'
);

describe('database maintenance UI integrity', () => {
  it('stacks offline maintenance commands into one column on narrow screens', () => {
    const styles = readFileSync(bannerStylesPath, 'utf8');

    expect(styles).toContain('@media (max-width: 680px)');
    expect(styles).toMatch(/\.commandGrid\s*\{\s*grid-template-columns:\s*1fr;/s);
  });
});
