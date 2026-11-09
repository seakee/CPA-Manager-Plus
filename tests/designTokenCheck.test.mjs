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
  stripComments,
  validateBaseline,
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
  it('R1 flags undefined variables, including fallback and multiline forms, and accepts definitions only from CSS/SCSS declarations and the runtime property registry', () => {
    const root = createFixtureTree({
      'styles/themes.scss': `:root {\n  --defined-in-scss: #2563eb;\n}\n`,
      'components/Widget.tsx': [
        `export function Widget(styles: Record<string, string>) {`,
        `  document.documentElement.style.setProperty('--registered-runtime', '12px');`,
        `  document.documentElement.style.setProperty('--unregistered-set-prop', '4px');`,
        `  const readOnly = styles['--not-defined-by-read'];`,
        `  const cssRef = 'var(--not-defined-by-read)';`,
        `  return <div data-x={readOnly} data-y={cssRef} style={{ '--unregistered-inline': '1' }} />;`,
        `}`,
      ].join('\n'),
      'components/Widget.module.scss': [
        `.widget {`,
        `  color: var(--defined-in-scss);`,
        `  padding: var(--registered-runtime);`,
        `  opacity: var(--unregistered-inline);`,
        `  margin: var(--unregistered-set-prop);`,
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

    const findings = collectFindings(root, { runtimeProperties: ['--registered-runtime'] });

    expect(findings.rules['undefined-var']).toEqual({
      'components/Widget.module.scss': {
        '--missing-direct': 1,
        '--missing-multiline': 1,
        '--missing-with-fallback': 1,
        '--unregistered-inline': 1,
        '--unregistered-set-prop': 1,
      },
      'components/Widget.tsx': {
        '--not-defined-by-read': 1,
      },
    });
    expect(findings.staleRuntimeProperties).toEqual([]);

    const comparison = compareToBaseline(findings, {
      version: 2,
      rules: emptyRules,
      scssRadiusDefinitions: {},
    });
    expect(comparison.passed).toBe(false);
    expect(
      comparison.failures
        .filter((item) => item.rule === 'undefined-var')
        .map((item) => [item.file, item.line, item.name])
    ).toEqual([
      ['components/Widget.module.scss', 6, '--missing-direct'],
      ['components/Widget.module.scss', 8, '--missing-multiline'],
      ['components/Widget.module.scss', 7, '--missing-with-fallback'],
      ['components/Widget.module.scss', 4, '--unregistered-inline'],
      ['components/Widget.module.scss', 5, '--unregistered-set-prop'],
      ['components/Widget.tsx', 5, '--not-defined-by-read'],
    ]);
  });

  it('R1 hard-fails registered runtime properties that no TS/TSX file sets as a quoted literal', () => {
    const root = createFixtureTree({
      'components/Live.tsx': `export const Live = () => <div style={{ '--live': 1 }} />;\n`,
      'components/Notes.tsx': `// '--commented-out' is not a use\nexport const note = 'plain';\n`,
      'components/Live.module.scss': `.live { width: var(--live); height: var(--gone); }\n`,
    });

    const findings = collectFindings(root, {
      runtimeProperties: ['--live', '--gone', '--commented-out'],
    });
    expect(findings.rules['undefined-var']).toEqual({});
    expect(findings.staleRuntimeProperties).toEqual(['--commented-out', '--gone']);

    const comparison = compareToBaseline(findings, {
      version: 2,
      rules: emptyRules,
      scssRadiusDefinitions: {},
    });
    expect(comparison.passed).toBe(false);
    expect(comparison.hardFailures.map((item) => [item.rule, item.name])).toEqual([
      ['runtime-property', '--commented-out'],
      ['runtime-property', '--gone'],
    ]);
  });

  it('CLI reads the runtime property registry next to the baseline and fails closed on an explicit missing or invalid registry', () => {
    const root = createFixtureTree({
      'components/Bar.tsx': `export const Bar = () => <div style={{ '--bar-width': '1px' }} />;\n`,
      'components/Bar.module.scss': `.bar { width: var(--bar-width); }\n`,
    });
    const ciDir = path.join(root, 'ci');
    mkdirSync(ciDir);
    const baselineFile = path.join(ciDir, 'design-token-baseline.json');
    writeFileSync(
      baselineFile,
      JSON.stringify({ version: 2, rules: emptyRules, scssRadiusDefinitions: {} }),
      'utf8'
    );
    const run = (...extra) =>
      spawnSync(process.execPath, [scriptPath, '--root', root, '--baseline', baselineFile, ...extra], {
        encoding: 'utf8',
      });

    const withoutRegistry = run();
    expect(withoutRegistry.status).toBe(1);
    expect(withoutRegistry.stderr).toContain('undefined CSS variable "--bar-width"');

    const registryFile = path.join(ciDir, 'design-token-runtime-properties.json');
    writeFileSync(
      registryFile,
      JSON.stringify({ version: 1, properties: { '--bar-width': 'components/Bar.tsx: inline style' } }),
      'utf8'
    );
    expect(run().status).toBe(0);

    const explicitMissing = run('--runtime-properties', path.join(ciDir, 'absent.json'));
    expect(explicitMissing.status).toBe(1);
    expect(explicitMissing.stderr).toContain('Failed to read runtime property registry');

    writeFileSync(registryFile, JSON.stringify({ version: 1, properties: { 'bar-width': '' } }), 'utf8');
    const invalid = run();
    expect(invalid.status).toBe(1);
    expect(invalid.stderr).toContain('Invalid custom property name in runtime property registry');
  });

  it('CLI hard-fails when the runtime property registry has an unsupported version', () => {
    const root = createFixtureTree({
      'components/Bar.tsx': `export const Bar = () => <div style={{ '--bar-width': '1px' }} />;\n`,
      'components/Bar.module.scss': `.bar { width: var(--bar-width); }\n`,
    });
    const ciDir = path.join(root, 'ci');
    mkdirSync(ciDir);
    const baselineFile = path.join(ciDir, 'design-token-baseline.json');
    const registryFile = path.join(ciDir, 'custom-runtime-properties.json');
    writeFileSync(
      baselineFile,
      JSON.stringify({ version: 2, rules: emptyRules, scssRadiusDefinitions: {} }),
      'utf8'
    );
    writeFileSync(
      registryFile,
      JSON.stringify({ version: 2, properties: { '--bar-width': 'components/Bar.tsx: inline style' } }),
      'utf8'
    );

    const result = spawnSync(
      process.execPath,
      [scriptPath, '--root', root, '--baseline', baselineFile, '--runtime-properties', registryFile],
      { encoding: 'utf8' }
    );
    expect(result.status).toBe(1);
    expect(result.stderr).toContain('Invalid runtime property registry');
    expect(result.stderr).toContain('Unsupported runtime property registry version: expected 1, got 2');
  });

  it('CLI hard-fails when a runtime property registry entry has an empty or whitespace-only note', () => {
    const root = createFixtureTree({
      'components/Bar.tsx': `export const Bar = () => <div style={{ '--bar-width': '1px' }} />;\n`,
      'components/Bar.module.scss': `.bar { width: var(--bar-width); }\n`,
    });
    const ciDir = path.join(root, 'ci');
    mkdirSync(ciDir);
    const baselineFile = path.join(ciDir, 'design-token-baseline.json');
    const registryFile = path.join(ciDir, 'custom-runtime-properties.json');
    writeFileSync(
      baselineFile,
      JSON.stringify({ version: 2, rules: emptyRules, scssRadiusDefinitions: {} }),
      'utf8'
    );

    for (const invalidNote of ['', '   \t\n  ']) {
      writeFileSync(
        registryFile,
        JSON.stringify({ version: 1, properties: { '--bar-width': invalidNote } }),
        'utf8'
      );
      const result = spawnSync(
        process.execPath,
        [scriptPath, '--root', root, '--baseline', baselineFile, '--runtime-properties', registryFile],
        { encoding: 'utf8' }
      );
      expect(result.status).toBe(1);
      expect(result.stderr).toContain('Invalid runtime property registry');
      expect(result.stderr).toContain(
        'Runtime property "--bar-width" needs a non-empty note naming where it is set'
      );
    }
  });

  it('CLI hard-fails on a stale runtime property registry entry even when running --update-baseline --allow-increase', () => {
    const root = createFixtureTree({
      'components/Bar.tsx': `export const Bar = () => <div style={{ '--bar-width': '1px' }} />;\n`,
      'components/Bar.module.scss': `.bar { width: var(--bar-width); border-radius: 14px; }\n`,
    });
    const ciDir = path.join(root, 'ci');
    mkdirSync(ciDir);
    const baselineFile = path.join(ciDir, 'design-token-baseline.json');
    const registryFile = path.join(ciDir, 'custom-runtime-properties.json');
    const initialBaseline = { version: 2, rules: emptyRules, scssRadiusDefinitions: {} };
    writeFileSync(baselineFile, JSON.stringify(initialBaseline, null, 2), 'utf8');
    writeFileSync(
      registryFile,
      JSON.stringify({
        version: 1,
        properties: {
          '--bar-width': 'components/Bar.tsx: inline style',
          '--stale-prop': 'components/Removed.tsx: former inline style',
        },
      }),
      'utf8'
    );

    const updateWithStale = spawnSync(
      process.execPath,
      [
        scriptPath,
        '--root',
        root,
        '--baseline',
        baselineFile,
        '--runtime-properties',
        registryFile,
        '--update-baseline',
        '--allow-increase',
      ],
      { encoding: 'utf8' }
    );
    expect(updateWithStale.status).toBe(1);
    expect(updateWithStale.stderr).toContain(
      'bin/ci/design-token-runtime-properties.json:1 runtime-property registered runtime custom property "--stale-prop" is not set as a quoted literal in any scanned TS/TSX file; remove the stale entry'
    );
    expect(JSON.parse(readFileSync(baselineFile, 'utf8'))).toEqual(initialBaseline);
  });

  it('R4 counts $radius-* evaluated through Sass interpolation inside strings and url(), but not plain string text or string literals inside the interpolation', () => {
    const root = createFixtureTree({
      'components/Badge.module.scss': [
        `.interpolated::after {`,
        `  content: "radius: #{$radius-sm}";`,
        `  background: url("/asset-#{$radius-sm}.svg");`,
        `}`,
        `.plain::after {`,
        `  content: "$radius-sm var(--plain-text) !important";`,
        `  background: url("/$radius-sm.svg");`,
        `}`,
        `.literalInInterpolation::after {`,
        `  content: "radius: #{'$radius-sm'} #{'var(--literal-in-interp)'}";`,
        `}`,
      ].join('\n'),
    });

    const { rules } = collectFindings(root);
    expect(rules['scss-radius-var']).toEqual({ 'components/Badge.module.scss': 2 });
    expect(rules['undefined-var']).toEqual({});
    expect(rules.important).toEqual({});
  });

  it('R3 checks font shorthand weights only before the font-size (including a unitless zero), treating later words as family names', () => {
    const root = createFixtureTree({
      'components/Type.module.scss': [
        `.familyBolder { font: 14px bolder, sans-serif; }`,
        `.familyLighter { font: 14px/1.4 lighter, sans-serif; }`,
        `.familyAfterZero { font: 0 bolder, sans-serif; }`,
        `.allowed { font: italic 600 small serif; }`,
        `.weightBolder { font: bolder 14px sans-serif; }`,
        `.weightNumeric { font: italic 650 1rem/1.5 serif; }`,
        `.weightBeforeZero { font: bolder 0 sans-serif; }`,
      ].join('\n'),
    });

    expect(collectFindings(root).rules['font-weight']).toEqual({
      'components/Type.module.scss': 3,
    });
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
      version: 2,
      rules: {
        ...emptyRules,
        'undefined-var': {
          'features/Page.module.scss': {
            '--brand-new-missing': 1,
            '--legacy-shadow': 1,
          },
        },
      },
      scssRadiusDefinitions: {},
    };
    expect(compareToBaseline(findings, matchingBaseline).passed).toBe(true);

    const swappedNameBaseline = {
      version: 2,
      rules: {
        ...emptyRules,
        'undefined-var': {
          'features/Page.module.scss': {
            '--legacy-shadow': 2,
          },
        },
      },
      scssRadiusDefinitions: {},
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
    [
      'R1-01 flags undefined CSS variables when the same key appears only in a non-style TS/TSX object literal',
      {
        'features/demo/notAStyle.tsx': `export const translations = { '--missing-design-token': 'display text' };\n`,
        'features/demo/Demo.module.scss': `.demo { color: var(--missing-design-token); }\n`,
      },
      {
        ...emptyRules,
        'undefined-var': {
          'features/demo/Demo.module.scss': { '--missing-design-token': 1 },
        },
      },
    ],
    [
      'R1-02 ignores var(), !important, and $radius-* inside quoted strings and url() in .css/.scss while inspecting #{} bodies',
      {
        'a.scss': [
          `.tip::after {`,
          `  content: "var(--example-not-real) !important $radius-md";`,
          `  background-image: url("https://example.org/var(--url-fragment).svg?x=!important&$radius-md");`,
          `}`,
        ].join('\n'),
      },
      emptyRules,
    ],
    [
      'R1-03 flags disallowed weights in font shorthand while allowing inherit, system font keywords, and var()/$ expressions',
      {
        'flagged.scss': [
          `.a { font: 900 14px Arial, sans-serif; }`,
          `.b { font: italic bolder 13px/1.4 Inter, sans-serif; }`,
          `.c { font: lighter 12px sans-serif; }`,
        ].join('\n'),
        'allowed.scss': [
          `:root { --font-shorthand: 500 14px sans-serif; }`,
          `.d { font: inherit; }`,
          `.e { font: caption; }`,
          `.f { font: 600 14px/2 Arial, sans-serif; }`,
          `.g { font: var(--font-shorthand); }`,
          `.h { font: $font-shorthand; }`,
        ].join('\n'),
      },
      {
        ...emptyRules,
        'font-weight': { 'flagged.scss': 3 },
      },
    ],
    [
      'R1-06 counts oklch, oklab, lab, lch, hwb, and color() in R5 while ignoring color-mix() without literals, background-color(), and named colours',
      {
        'modern.scss': [
          `.demo {`,
          `  color: oklch(60% 0.1 240);`,
          `  background: color(display-p3 1 0 0);`,
          `  border-color: hwb(120 10% 20%);`,
          `  outline-color: lab(50% 40 59.5);`,
          `  fill: lch(52.2% 72.2 50);`,
          `  stroke: oklab(0.6 0.1 -0.1);`,
          `  box-shadow: 0 0 0 1px color-mix(in srgb, var(--defined-a) 20%, transparent);`,
          `  caret-color: red;`,
          `  filter: background-color(var(--defined-a));`,
          `}`,
        ].join('\n'),
        'styles/themes.scss': `:root { --defined-a: #123456; }\n`,
      },
      {
        ...emptyRules,
        'color-literal': { 'modern.scss': 6 },
      },
    ],
    [
      'R1-07 treats // as a comment in .scss and script files but not in .css',
      {
        'plain.css': `.demo {\n  --link-text: https://example.org; color: #123456;\n}\n`,
        'styled.scss': `.demo {\n  --link-text: https://example.org; color: #123456;\n}\n`,
      },
      {
        ...emptyRules,
        'color-literal': { 'plain.css': 1 },
      },
    ],
  ])('%s', (_label, files, expectedRules) => {
    const root = createFixtureTree(files);
    const findings = collectFindings(root);
    expect(findings.rules).toEqual(expectedRules);
  });

  it('R4 freezes legacy $radius-* names and raw values in styles/variables.scss and hard-fails on new or modified definitions even with --allow-increase while allowing removals', () => {
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
    expect(initialFindings.scssRadiusDefinitions).toEqual({
      '$radius-md': '8px',
      '$radius-sm': '4px',
    });

    const initialBaseline = buildBaselineFromFindings(initialFindings);
    writeFileSync(baselineFile, JSON.stringify(initialBaseline, null, 2), 'utf8');

    // 1. Changing an existing definition's value ($radius-sm: 4px -> 14px) hard-fails
    writeFileSync(
      path.join(root, 'styles/variables.scss'),
      `$radius-sm: 14px;\n$radius-md: 8px;\n`,
      'utf8'
    );
    const mutatedValueFindings = collectFindings(root);
    const mutatedValueComparison = compareToBaseline(mutatedValueFindings, initialBaseline);
    expect(mutatedValueComparison.passed).toBe(false);
    expect(mutatedValueComparison.hardFailures).toEqual([
      expect.objectContaining({
        file: 'styles/variables.scss',
        line: 1,
        rule: 'scss-radius-var',
        name: '$radius-sm',
        current: '14px',
        baseline: '4px',
      }),
    ]);

    const refusedValueChangeCli = spawnSync(
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
    expect(refusedValueChangeCli.status).toBe(1);
    expect(refusedValueChangeCli.stderr).toContain(
      'SCSS radius variable "$radius-sm" value changed from "4px" to "14px"'
    );

    // 2. Adding a new $radius-* definition hard-fails
    writeFileSync(
      path.join(root, 'styles/variables.scss'),
      `$radius-sm: 4px;\n$radius-md: 8px;\n$radius-xl: 24px;\n`,
      'utf8'
    );

    const addedDefFindings = collectFindings(root);
    const addedDefComparison = compareToBaseline(addedDefFindings, initialBaseline);
    expect(addedDefComparison.passed).toBe(false);
    expect(addedDefComparison.hardFailures).toEqual([
      expect.objectContaining({
        file: 'styles/variables.scss',
        line: 3,
        rule: 'scss-radius-var',
        name: '$radius-xl',
      }),
    ]);

    const refusedNewDefCli = spawnSync(
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
    expect(refusedNewDefCli.status).toBe(1);
    expect(refusedNewDefCli.stderr).toContain(
      'new SCSS radius variable definition "$radius-xl" is not allowed in variables.scss'
    );
    const preservedBaseline = JSON.parse(readFileSync(baselineFile, 'utf8'));
    expect(preservedBaseline.scssRadiusDefinitions).toEqual({
      '$radius-md': '8px',
      '$radius-sm': '4px',
    });

    // 3. Removing a legacy definition is allowed and tightens the baseline
    writeFileSync(path.join(root, 'styles/variables.scss'), `$radius-md: 8px;\n`, 'utf8');
    const removedDefFindings = collectFindings(root);
    const removedDefComparison = compareToBaseline(removedDefFindings, initialBaseline);
    expect(removedDefComparison.passed).toBe(true);
    expect(removedDefComparison.hardFailures).toHaveLength(0);
  });

  it('R1-05 fails closed on missing, unreadable, wrong-version, or schema-incomplete baselines and requires --init without overwriting', () => {
    const root = createFixtureTree({
      'styles/variables.scss': `$radius-sm: 4px;\n`,
      'components/Box.module.scss': `.box { border-radius: 8px; }\n`,
    });
    const baselineFile = path.join(root, 'baseline.json');

    // Missing baseline fails both normal check and --update-baseline --allow-increase
    const missingCheck = spawnSync(
      process.execPath,
      [scriptPath, '--root', root, '--baseline', baselineFile],
      { encoding: 'utf8' }
    );
    expect(missingCheck.status).toBe(1);
    expect(missingCheck.stderr).toContain('Missing design token baseline file');

    const missingUpdate = spawnSync(
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
    expect(missingUpdate.status).toBe(1);
    expect(missingUpdate.stderr).toContain('Missing design token baseline file');

    // --init creates the baseline when it does not exist
    const initCreate = spawnSync(
      process.execPath,
      [scriptPath, '--root', root, '--baseline', baselineFile, '--init'],
      { encoding: 'utf8' }
    );
    expect(initCreate.status).toBe(0);
    const createdBaseline = JSON.parse(readFileSync(baselineFile, 'utf8'));
    expect(createdBaseline.version).toBe(2);
    expect(createdBaseline.scssRadiusDefinitions).toEqual({ '$radius-sm': '4px' });

    // --init refuses to overwrite an existing baseline file
    const initOverwrite = spawnSync(
      process.execPath,
      [scriptPath, '--root', root, '--baseline', baselineFile, '--init'],
      { encoding: 'utf8' }
    );
    expect(initOverwrite.status).toBe(1);
    expect(initOverwrite.stderr).toContain(
      'Refusing to overwrite existing design token baseline with --init'
    );

    // Wrong version or missing scssRadiusDefinitions fails closed
    const findings = collectFindings(root);
    const wrongVersionBaseline = { ...createdBaseline, version: 1 };
    expect(validateBaseline(wrongVersionBaseline)).toEqual(
      expect.arrayContaining([expect.stringContaining('Unsupported baseline version')])
    );
    expect(compareToBaseline(findings, wrongVersionBaseline).passed).toBe(false);

    const missingRadiusDefsBaseline = {
      version: 2,
      rules: createdBaseline.rules,
    };
    writeFileSync(baselineFile, JSON.stringify(missingRadiusDefsBaseline, null, 2), 'utf8');
    const invalidSchemaUpdate = spawnSync(
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
    expect(invalidSchemaUpdate.status).toBe(1);
    expect(invalidSchemaUpdate.stderr).toContain('scssRadiusDefinitions');
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

  it('maintains WCAG AA (>= 4.5:1) contrast for .btn-danger text against --danger-solid, --danger-solid-hover, and --danger-solid-active in light and dark themes', () => {
    const themesScss = stripComments(
      readFileSync(path.join(repoRoot, 'apps/web/src/styles/themes.scss'), 'utf8'),
      { isScript: false, allowLineComments: true }
    );
    const componentsScss = stripComments(
      readFileSync(path.join(repoRoot, 'apps/web/src/styles/components.scss'), 'utf8'),
      { isScript: false, allowLineComments: true }
    );

    const listTopLevelBlocks = (source) => {
      const blocks = [];
      let headerStart = 0;
      let cursor = 0;
      while (cursor < source.length) {
        const ch = source[cursor];
        if (ch === ';') {
          headerStart = cursor + 1;
          cursor += 1;
          continue;
        }
        if (ch === '{') {
          const selectorHeader = source.slice(headerStart, cursor).trim();
          const openBrace = cursor;
          let depth = 1;
          cursor += 1;
          while (cursor < source.length && depth > 0) {
            if (source[cursor] === '{') depth += 1;
            else if (source[cursor] === '}') depth -= 1;
            cursor += 1;
          }
          const body = source.slice(openBrace + 1, cursor - 1);
          const selectors = selectorHeader
            .split(',')
            .map((item) => item.trim().replace(/\s+/g, ' '))
            .filter(Boolean);
          blocks.push({ selectorHeader, selectors, body });
          headerStart = cursor;
          continue;
        }
        cursor += 1;
      }
      return blocks;
    };

    const normalizeSelector = (selector) =>
      selector
        .trim()
        .replace(/['"]/g, "'")
        .replace(/\s+/g, ' ');

    const findMatchingBlocks = (source, targetSelector, { optional = false } = {}) => {
      const normalizedTarget = normalizeSelector(targetSelector);
      const matches = listTopLevelBlocks(source).filter((block) =>
        block.selectors.some((sel) => normalizeSelector(sel) === normalizedTarget)
      );
      if (!optional && matches.length === 0) {
        throw new Error(`Selector block not found for selector item: ${targetSelector}`);
      }
      return matches.map((block) => block.body);
    };

    const extractDirectDeclarations = (blockBodies) => {
      const bodies = Array.isArray(blockBodies) ? blockBodies : [blockBodies];
      const declarations = {};
      for (const blockBody of bodies) {
        let directText = '';
        let depth = 0;
        for (let i = 0; i < blockBody.length; i += 1) {
          const ch = blockBody[i];
          if (ch === '{') {
            depth += 1;
          } else if (ch === '}') {
            depth = Math.max(0, depth - 1);
          } else if (depth === 0) {
            directText += ch;
          }
        }
        for (const match of directText.matchAll(/([A-Za-z_-][A-Za-z0-9_-]*)\s*:\s*([^;]+);/g)) {
          declarations[match[1].trim()] = match[2].trim();
        }
      }
      return declarations;
    };

    // Verify grouped selector lists (e.g. `&.btn-danger, &.btn-critical {`) are matched per item.
    expect(
      extractDirectDeclarations(
        findMatchingBlocks('&.btn-danger, &.btn-critical { color: var(--primary-contrast); }', '&.btn-danger')
      )
    ).toEqual({ color: 'var(--primary-contrast)' });

    const rootTokens = extractDirectDeclarations(findMatchingBlocks(themesScss, ':root'));
    const darkOverrides = extractDirectDeclarations(
      findMatchingBlocks(themesScss, "[data-theme='dark']")
    );
    const themeTokenMaps = {
      light: rootTokens,
      dark: { ...rootTokens, ...darkOverrides },
    };

    const extractScopedDangerRules = (scopeSource) => {
      const btnBlocks = findMatchingBlocks(scopeSource, '.btn', { optional: true });
      const btnDecls = extractDirectDeclarations(btnBlocks);
      const nestedDangerBlocks = btnBlocks.flatMap((block) =>
        findMatchingBlocks(block, '&.btn-danger', { optional: true })
      );
      const flatDangerBlocks = findMatchingBlocks(scopeSource, '.btn.btn-danger', { optional: true });
      const dangerBlocks = [...nestedDangerBlocks, ...flatDangerBlocks];
      const dangerDecls = extractDirectDeclarations(dangerBlocks);
      const hoverBlocks = [
        ...dangerBlocks.flatMap((block) => findMatchingBlocks(block, '&:hover', { optional: true })),
        ...findMatchingBlocks(scopeSource, '.btn.btn-danger:hover', { optional: true }),
      ];
      const activeBlocks = [
        ...dangerBlocks.flatMap((block) => findMatchingBlocks(block, '&:active', { optional: true })),
        ...findMatchingBlocks(scopeSource, '.btn.btn-danger:active', { optional: true }),
      ];
      return {
        btnDecls,
        dangerDecls,
        hoverDecls: extractDirectDeclarations(hoverBlocks),
        activeDecls: extractDirectDeclarations(activeBlocks),
      };
    };

    const baseRules = extractScopedDangerRules(componentsScss);
    const darkScopeBlocks = findMatchingBlocks(componentsScss, "[data-theme='dark']", {
      optional: true,
    });
    const darkRules = extractScopedDangerRules(darkScopeBlocks.join('\n'));

    const extractVarTokenName = (valueExpr) => {
      const match = /^var\(\s*(--[A-Za-z0-9_-]+)\s*(?:,[^)]+)?\)$/.exec(valueExpr ?? '');
      if (!match) {
        throw new Error(`Expected single CSS var() token reference, got: ${JSON.stringify(valueExpr)}`);
      }
      return match[1];
    };

    const restingBgToken = extractVarTokenName(
      baseRules.dangerDecls['background-color'] ?? baseRules.dangerDecls.background
    );
    const hoverBgToken = extractVarTokenName(
      baseRules.hoverDecls['background-color'] ?? baseRules.hoverDecls.background
    );
    const activeBgToken = extractVarTokenName(
      baseRules.activeDecls['background-color'] ?? baseRules.activeDecls.background
    );
    expect([restingBgToken, hoverBgToken, activeBgToken]).toEqual([
      '--danger-solid',
      '--danger-solid-hover',
      '--danger-solid-active',
    ]);

    const lightBaseTextExpr = baseRules.dangerDecls.color ?? baseRules.btnDecls.color;
    const darkBaseTextExpr =
      darkRules.dangerDecls.color ??
      baseRules.dangerDecls.color ??
      darkRules.btnDecls.color ??
      baseRules.btnDecls.color;

    const stateRulesByTheme = {
      light: [
        { bgToken: restingBgToken, textExpr: lightBaseTextExpr },
        { bgToken: hoverBgToken, textExpr: baseRules.hoverDecls.color ?? lightBaseTextExpr },
        { bgToken: activeBgToken, textExpr: baseRules.activeDecls.color ?? lightBaseTextExpr },
      ],
      dark: [
        { bgToken: restingBgToken, textExpr: darkBaseTextExpr },
        {
          bgToken: hoverBgToken,
          textExpr: darkRules.hoverDecls.color ?? baseRules.hoverDecls.color ?? darkBaseTextExpr,
        },
        {
          bgToken: activeBgToken,
          textExpr: darkRules.activeDecls.color ?? baseRules.activeDecls.color ?? darkBaseTextExpr,
        },
      ],
    };

    const parseCssColorToRgb = (rawColor) => {
      const value = rawColor.trim();
      const hex6 = /^#([0-9a-fA-F]{2})([0-9a-fA-F]{2})([0-9a-fA-F]{2})$/.exec(value);
      if (hex6) {
        return [
          Number.parseInt(hex6[1], 16),
          Number.parseInt(hex6[2], 16),
          Number.parseInt(hex6[3], 16),
        ];
      }
      const hex3 = /^#([0-9a-fA-F])([0-9a-fA-F])([0-9a-fA-F])$/.exec(value);
      if (hex3) {
        return [
          Number.parseInt(hex3[1] + hex3[1], 16),
          Number.parseInt(hex3[2] + hex3[2], 16),
          Number.parseInt(hex3[3] + hex3[3], 16),
        ];
      }
      const rgbMatch = /^rgb\(\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})\s*\)$/.exec(value);
      if (rgbMatch) {
        return [
          Number.parseInt(rgbMatch[1], 10),
          Number.parseInt(rgbMatch[2], 10),
          Number.parseInt(rgbMatch[3], 10),
        ];
      }
      throw new Error(`Unsupported or unresolved color value: ${JSON.stringify(rawColor)}`);
    };

    const resolveColorInTheme = (exprOrToken, tokenMap) => {
      let current = exprOrToken.trim();
      if (current.startsWith('--')) {
        current = `var(${current})`;
      }
      const seen = new Set();
      while (true) {
        const varMatch = /^var\(\s*(--[A-Za-z0-9_-]+)\s*(?:,\s*([^)]+))?\)$/.exec(current);
        if (!varMatch) break;
        const [, tokenName, fallback] = varMatch;
        if (seen.has(tokenName)) {
          throw new Error(`Circular token reference involving ${tokenName}`);
        }
        seen.add(tokenName);
        const nextValue = tokenMap[tokenName] ?? fallback?.trim();
        if (!nextValue) {
          throw new Error(`Undefined token ${tokenName}`);
        }
        current = nextValue;
      }
      return parseCssColorToRgb(current);
    };

    const toLinearChannel = (channel8Bit) => {
      const srgb = channel8Bit / 255;
      return srgb <= 0.04045 ? srgb / 12.92 : ((srgb + 0.055) / 1.055) ** 2.4;
    };

    const relativeLuminance = ([r, g, b]) =>
      0.2126 * toLinearChannel(r) + 0.7152 * toLinearChannel(g) + 0.0722 * toLinearChannel(b);

    const wcagContrastRatio = (rgbA, rgbB) => {
      const lumA = relativeLuminance(rgbA);
      const lumB = relativeLuminance(rgbB);
      const lighter = Math.max(lumA, lumB);
      const darker = Math.min(lumA, lumB);
      return (lighter + 0.05) / (darker + 0.05);
    };

    for (const [themeName, tokenMap] of Object.entries(themeTokenMaps)) {
      for (const { bgToken, textExpr } of stateRulesByTheme[themeName]) {
        const textRgb = resolveColorInTheme(textExpr, tokenMap);
        const bgRgb = resolveColorInTheme(bgToken, tokenMap);
        const ratio = wcagContrastRatio(textRgb, bgRgb);
        expect(
          ratio,
          `.btn-danger text (${textExpr}) vs ${bgToken} in ${themeName} theme must be >= 4.5:1 (got ${ratio.toFixed(2)}:1)`
        ).toBeGreaterThanOrEqual(4.5);
      }
    }
  });
});
