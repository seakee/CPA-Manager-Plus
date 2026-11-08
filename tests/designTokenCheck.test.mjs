import { execFileSync, spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterEach, describe, expect, it } from 'vitest';
import {
  buildBaselineFromFindings,
  collectFindings,
  compareToBaseline,
} from '../bin/ci/check-design-tokens.mjs';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const scriptPath = path.join(repoRoot, 'bin/ci/check-design-tokens.mjs');

const tempDirs = [];

const createFixtureTree = (files) => {
  const root = mkdtempSync(path.join(tmpdir(), 'cpamp-design-tokens-'));
  tempDirs.push(root);

  for (const [relativePath, content] of Object.entries(files)) {
    const fullPath = path.join(root, relativePath);
    mkdirSync(path.dirname(fullPath), { recursive: true });
    writeFileSync(fullPath, content, 'utf8');
  }

  return root;
};

const emptyRules = {
  'undefined-var': {},
  radius: {},
  'font-weight': {},
  'scss-radius-var': {},
  'color-literal': {},
  important: {},
};

afterEach(() => {
  while (tempDirs.length > 0) {
    rmSync(tempDirs.pop(), { recursive: true, force: true });
  }
});

describe('design token checker', () => {
  it('R1 flags undefined variables, including fallback and multiline forms, and accepts definitions from SCSS, TSX inline styles, assignment, and setProperty while rejecting plain computed reads', () => {
    const root = createFixtureTree({
      'styles/themes.scss': `:root {\n  --defined-in-scss: #2563eb;\n}\n`,
      'components/Widget.tsx': [
        `export function Widget(styles: Record<string, string>, el: HTMLElement) {`,
        `  document.documentElement.style.setProperty('--defined-via-set-prop', '12px');`,
        `  el.style['--defined-via-assignment'] = '8px';`,
        `  const readOnly = styles['--not-defined-by-read'];`,
        `  const cssRef = 'var(--not-defined-by-read)';`,
        `  const dynamicKey = '--defined-via-template';`,
        `  const style = {`,
        `    '--defined-in-tsx': '1',`,
        `    ['--defined-in-brackets']: '2',`,
        '    [`--defined-in-backticks`]: `var(--${dynamicKey})`,',
        `  };`,
        `  return <div data-x={readOnly} data-y={cssRef} style={style} />;`,
        `}`,
      ].join('\n'),
      'components/Widget.module.scss': [
        `.widget {`,
        `  color: var(--defined-in-scss);`,
        `  padding: var(--defined-via-set-prop);`,
        `  margin: var(--defined-via-assignment);`,
        `  opacity: var(--defined-in-tsx);`,
        `  z-index: var(--defined-in-brackets);`,
        `  line-height: var(--defined-in-backticks);`,
        `  background: var(--missing-direct);`,
        `  border-color: var(--missing-with-fallback, var(--defined-in-scss));`,
        `  outline-color: var(`,
        `    --missing-multiline`,
        `  );`,
        `}`,
      ].join('\n'),
      'components/Widget.test.tsx': `const ignoredTest = 'var(--ignored-in-test)';\n`,
      'components/Widget.spec.tsx': `const ignoredSpec = 'var(--ignored-in-spec)';\n`,
      'components/__tests__/Helper.tsx': `const ignoredDir = 'var(--ignored-in-tests-dir)';\n`,
      'types/global.d.ts': `export type Ignored = 'var(--ignored-in-dts)';\n`,
    });

    const findings = collectFindings(root);

    expect(findings.rules['undefined-var']).toEqual({
      'components/Widget.module.scss': {
        '--missing-direct': 1,
        '--missing-multiline': 1,
        '--missing-with-fallback': 1,
      },
      'components/Widget.tsx': {
        '--not-defined-by-read': 1,
      },
    });

    const comparison = compareToBaseline(findings, { version: 1, rules: {} });
    expect(comparison.passed).toBe(false);
    expect(
      comparison.failures
        .filter((item) => item.rule === 'undefined-var')
        .map((item) => [item.file, item.line, item.name])
    ).toEqual([
      ['components/Widget.module.scss', 8, '--missing-direct'],
      ['components/Widget.module.scss', 10, '--missing-multiline'],
      ['components/Widget.module.scss', 9, '--missing-with-fallback'],
      ['components/Widget.tsx', 5, '--not-defined-by-read'],
    ]);
  });

  it('R1 enforces a per-file and per-variable ratchet so an unknown variable name in an already-baselined file fails', () => {
    const root = createFixtureTree({
      'styles/themes.scss': `:root {\n  --app-surface: #fff;\n}\n`,
      'features/Page.module.scss': [
        `.card {`,
        `  box-shadow: var(--legacy-shadow);`,
        `  color: var(--brand-new-missing);`,
        `}`,
      ].join('\n'),
    });

    const findings = collectFindings(root);

    const matchingBaseline = {
      version: 1,
      rules: {
        'undefined-var': {
          'features/Page.module.scss': {
            '--brand-new-missing': 1,
            '--legacy-shadow': 1,
          },
        },
      },
    };
    expect(compareToBaseline(findings, matchingBaseline).passed).toBe(true);

    const swappedNameBaseline = {
      version: 1,
      rules: {
        'undefined-var': {
          'features/Page.module.scss': {
            '--legacy-shadow': 2,
          },
        },
      },
    };
    const swappedResult = compareToBaseline(findings, swappedNameBaseline);
    expect(swappedResult.passed).toBe(false);
    expect(swappedResult.failures).toHaveLength(1);
    expect(swappedResult.failures[0]).toMatchObject({
      file: 'features/Page.module.scss',
      line: 3,
      rule: 'undefined-var',
      name: '--brand-new-missing',
    });
    expect(swappedResult.tightenNotices).toHaveLength(1);
    expect(swappedResult.tightenNotices[0]).toMatchObject({
      rule: 'undefined-var',
      file: 'features/Page.module.scss',
      name: '--legacy-shadow',
      current: 1,
      baseline: 2,
    });
  });

  it.each([
    [
      'R5 counts colour literals in @include arguments and ignores @include radius numbers',
      {
        'a.scss': `@include chip(#fff, rgba(1, 2, 3, 0.5));\n@include rounded(14px);\n`,
      },
      {
        ...emptyRules,
        'color-literal': { 'a.scss': 2 },
      },
    ],
    [
      'R5 counts colour literals in @return and @if/@else if conditions',
      {
        'a.scss': [
          `@function pick($tone) {`,
          `  @if $tone == #fff { @return #123456; }`,
          `  @else if $tone == rgba(0, 0, 0, 0.5) { @return #abcdef; }`,
          `}`,
        ].join('\n'),
      },
      {
        ...emptyRules,
        'color-literal': { 'a.scss': 4 },
      },
    ],
    [
      'R5 and R2 check both the base value and children of SCSS nested properties',
      {
        'a.scss': `.a { border: 1px solid #fff { radius: 14px; } font: { weight: 650; } }\n`,
      },
      {
        ...emptyRules,
        radius: { 'a.scss': 1 },
        'font-weight': { 'a.scss': 1 },
        'color-literal': { 'a.scss': 1 },
      },
    ],
    [
      'R5 counts colours inside #{#fff} interpolations while ignoring #{$sel} and selectors',
      {
        'a.scss': `#abc, .#{$sel} { color: #{#fff}; content: '#abc'; background: url(#grad); }\n`,
      },
      {
        ...emptyRules,
        'color-literal': { 'a.scss': 1 },
      },
    ],
    [
      'R1 preserves var() after nested template literals containing // and strips comments inside ${...}',
      {
        'a.tsx': [
          'const a = `${`//text`} var(--missing-after-nested-tpl)`;',
          "const b = `${/* var(--comment-only) */ 'ok'}`;",
          'export const tuple = [a, b];',
        ].join('\n'),
      },
      {
        ...emptyRules,
        'undefined-var': {
          'a.tsx': { '--missing-after-nested-tpl': 1 },
        },
      },
    ],
    [
      'R2 checks custom properties whose name contains radius while allowing scale tokens',
      {
        'styles/themes.scss': `:root { --app-radius-lg: 20px; --card-radius: 14px; }\n`,
        'a.scss': `.a { --border-radius: 3px; --other-metric: 14px; }\n`,
      },
      {
        ...emptyRules,
        radius: {
          'a.scss': 1,
          'styles/themes.scss': 1,
        },
      },
    ],
    [
      'R1 discovers block-valued custom properties and strings containing semicolons',
      {
        'a.scss': [
          `:root {`,
          `  --defined-block: { a: b };`,
          `  --sep: 'a; b';`,
          `  --after-sep: 12px;`,
          `}`,
          `.a { color: var(--defined-block); padding: var(--after-sep); }`,
        ].join('\n'),
      },
      emptyRules,
    ],
    [
      'scans keyframes, @media/@supports preludes, &:hover, and color-mix() in linear-gradient()',
      {
        'a.scss': [
          `@keyframes pulse {`,
          `  0% { opacity: 0; }`,
          `  50% { border-radius: 14px; font-weight: 650; color: #fff; }`,
          `}`,
          `@media (max-width: 768px) {`,
          `  @supports (display: grid) {`,
          `    .btn {`,
          `      &:hover {`,
          `        background: linear-gradient(120deg, color-mix(in srgb, #123456 20%, rgba(1, 2, 3, 0.5)), #abcdef);`,
          `      }`,
          `      border-radius: 10px;`,
          `    }`,
          `  }`,
          `}`,
        ].join('\n'),
      },
      {
        ...emptyRules,
        radius: { 'a.scss': 2 },
        'font-weight': { 'a.scss': 1 },
        'color-literal': { 'a.scss': 4 },
      },
    ],
    [
      'preserves quoted //, /* */, escaped quotes, and quoted url("...)//..."), and scans multiline declarations',
      {
        'a.scss': [
          `$m: (font-weight: 650, border-radius: 14px);`,
          `/* font-weight: 800; */`,
          `.banner {`,
          `  content: 'a // b /* c */ \\' // still string'; color: #fff;`,
          `  background-image: url("http://example.com/a)b//c.png"); border-radius: 8px`,
          `    14px; // font-weight: 900;`,
          `  border-top-left-radius: #{$r} 14px;`,
          `  border-bottom-right-radius: revert;`,
          `  font-weight:`,
          `    650;`,
          `  font-weight: revert-layer;`,
          `}`,
        ].join('\n'),
      },
      {
        ...emptyRules,
        radius: { 'a.scss': 2 },
        'font-weight': { 'a.scss': 1 },
        'color-literal': { 'a.scss': 1 },
      },
    ],
    [
      'R6 counts !important occurrences outside comments',
      {
        'a.scss': [
          `/* color: red !important; */`,
          `.a {`,
          `  display: flex !important;`,
          `  opacity: 1 !important; // !important`,
          `}`,
        ].join('\n'),
      },
      {
        ...emptyRules,
        important: { 'a.scss': 2 },
      },
    ],
  ])('%s', (_label, files, expectedRules) => {
    const root = createFixtureTree(files);
    const findings = collectFindings(root);
    expect(findings.rules).toEqual(expectedRules);
  });

  it('R4 counts $radius-* uses and hard-fails on new $radius-* definitions in styles/variables.scss even with --update-baseline --allow-increase', () => {
    const root = createFixtureTree({
      'styles/variables.scss': `$radius-sm: 4px;\n$radius-md: 8px;\n`,
      'components/Box.module.scss': `.box {\n  border-radius: $radius-md;\n}\n`,
    });
    const baselineFile = path.join(root, 'baseline.json');

    const initialFindings = collectFindings(root);
    expect(initialFindings.rules['scss-radius-var']).toEqual({
      'components/Box.module.scss': 1,
      'styles/variables.scss': 2,
    });
    expect(initialFindings.scssRadiusDefinitions).toEqual(['$radius-md', '$radius-sm']);

    const initialBaseline = buildBaselineFromFindings(initialFindings);
    writeFileSync(baselineFile, JSON.stringify(initialBaseline, null, 2), 'utf8');

    writeFileSync(
      path.join(root, 'styles/variables.scss'),
      `$radius-sm: 4px;\n$radius-md: 8px;\n$radius-xl: 24px;\n`,
      'utf8'
    );

    const updatedFindings = collectFindings(root);
    expect(updatedFindings.rules['scss-radius-var']).toEqual({
      'components/Box.module.scss': 1,
      'styles/variables.scss': 3,
    });
    const comparison = compareToBaseline(updatedFindings, initialBaseline);
    expect(comparison.passed).toBe(false);
    expect(comparison.hardFailures).toEqual([
      expect.objectContaining({
        file: 'styles/variables.scss',
        line: 3,
        rule: 'scss-radius-var',
        name: '$radius-xl',
      }),
    ]);

    const refusedCli = spawnSync(
      process.execPath,
      [
        scriptPath,
        '--root',
        root,
        '--baseline',
        baselineFile,
        '--update-baseline',
        '--allow-increase',
      ],
      { encoding: 'utf8' }
    );
    expect(refusedCli.status).toBe(1);
    expect(refusedCli.stderr).toContain(
      'new SCSS radius variable definition "$radius-xl" is not allowed in variables.scss'
    );
    const preservedBaseline = JSON.parse(readFileSync(baselineFile, 'utf8'));
    expect(preservedBaseline.scssRadiusDefinitions).toEqual(['$radius-md', '$radius-sm']);
  });

  it('R5 exempts only <root>/styles/themes.scss and still checks other themes.scss files', () => {
    const root = createFixtureTree({
      'styles/themes.scss': `:root {\n  --app-bg: #eff2f7;\n  --app-surface: rgba(255, 255, 255, 0.9);\n}\n`,
      'features/other/themes.scss': `.localTheme {\n  color: #123456;\n}\n`,
      'components/Chart.module.scss': [
        `$local-color: #abcdef;`,
        `$selector: 'chart';`,
        `#abc, #fff1, .#{$selector} {`,
        `  --local-prop: #112233;`,
        `  content: '#abc rgb(1, 2, 3)';`,
        `  clip-path: url(#clip-shape);`,
        `  color: #1e293b;`,
        `  background: rgba(15, 23, 42, 0.4);`,
        `  border-color: hsl(210, 40%, 50%);`,
        `}`,
      ].join('\n'),
    });

    const findings = collectFindings(root);
    expect(findings.rules['color-literal']).toEqual({
      'components/Chart.module.scss': 5,
      'features/other/themes.scss': 1,
    });
    expect(
      findings.occurrencesByRule['color-literal']['components/Chart.module.scss'].map((x) => x.line)
    ).toEqual([1, 4, 7, 8, 9]);
  });

  it('enforces ratchet semantics (above fails with remediation footer, equal passes, below warns, new file fails) and refuses --update-baseline increases without --allow-increase', () => {
    const root = createFixtureTree({
      'components/Existing.module.scss': `.a {\n  font-weight: 650;\n  color: #fff;\n}\n`,
    });
    const baselineFile = path.join(root, 'baseline.json');

    const initialFindings = collectFindings(root);
    const initialBaseline = buildBaselineFromFindings(initialFindings);
    writeFileSync(baselineFile, JSON.stringify(initialBaseline, null, 2), 'utf8');

    expect(compareToBaseline(initialFindings, initialBaseline).passed).toBe(true);

    const higherBaseline = {
      ...initialBaseline,
      rules: {
        ...initialBaseline.rules,
        'font-weight': { 'components/Existing.module.scss': 3 },
      },
    };
    const tightenedCheck = compareToBaseline(initialFindings, higherBaseline);
    expect(tightenedCheck.passed).toBe(true);
    expect(tightenedCheck.tightenNotices).toHaveLength(1);
    expect(tightenedCheck.tightenNotices[0]).toMatchObject({
      rule: 'font-weight',
      file: 'components/Existing.module.scss',
      current: 1,
      baseline: 3,
    });

    writeFileSync(
      path.join(root, 'components/NewFile.module.scss'),
      `.new {\n  border-radius: 14px;\n}\n`,
      'utf8'
    );
    const increasedFindings = collectFindings(root);
    const increasedComparison = compareToBaseline(increasedFindings, initialBaseline);
    expect(increasedComparison.passed).toBe(false);
    expect(increasedComparison.failures).toEqual([
      expect.objectContaining({
        file: 'components/NewFile.module.scss',
        line: 2,
        rule: 'radius',
      }),
    ]);

    const failingCheck = spawnSync(
      process.execPath,
      [scriptPath, '--root', root, '--baseline', baselineFile],
      { encoding: 'utf8' }
    );
    expect(failingCheck.status).toBe(1);
    expect(failingCheck.stderr).toContain('docs/development/ui-design-system.md');

    const refusedUpdate = spawnSync(
      process.execPath,
      [scriptPath, '--root', root, '--baseline', baselineFile, '--update-baseline'],
      { encoding: 'utf8' }
    );
    expect(refusedUpdate.status).toBe(1);
    expect(refusedUpdate.stderr).toContain('Refusing to update baseline');

    execFileSync(
      process.execPath,
      [
        scriptPath,
        '--root',
        root,
        '--baseline',
        baselineFile,
        '--update-baseline',
        '--allow-increase',
      ],
      { encoding: 'utf8' }
    );
    const updatedBaseline = JSON.parse(readFileSync(baselineFile, 'utf8'));
    expect(updatedBaseline.rules.radius['components/NewFile.module.scss']).toBe(1);

    const passingRun = spawnSync(
      process.execPath,
      [scriptPath, '--root', root, '--baseline', baselineFile],
      { encoding: 'utf8' }
    );
    expect(passingRun.status).toBe(0);
  });
});
