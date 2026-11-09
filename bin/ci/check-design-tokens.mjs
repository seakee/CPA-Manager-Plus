import { existsSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const defaultRootDir = path.join(repoRoot, 'apps/web/src');
const defaultBaselinePath = path.join(repoRoot, 'bin/ci/design-token-baseline.json');

const BASELINE_VERSION = 2;

const RATCHET_FILE_RULES = [
  'radius',
  'font-weight',
  'scss-radius-var',
  'color-literal',
  'important',
];
const ALL_RULE_KEYS = ['undefined-var', ...RATCHET_FILE_RULES];


const ALLOWED_RADIUS_PARTS = new Set([
  '0',
  '6px',
  '8px',
  '12px',
  '16px',
  '20px',
  '9999px',
  '50%',
  'inherit',
  'initial',
  'unset',
  'revert',
  'revert-layer',
]);

const ALLOWED_FONT_WEIGHTS = new Set([
  '400',
  '500',
  '600',
  '700',
  'normal',
  'bold',
  'inherit',
  'initial',
  'unset',
  'revert',
  'revert-layer',
]);

const toPosixPath = (filePath) => filePath.replaceAll(path.sep, '/');

const sortObjectKeys = (record) =>
  Object.fromEntries(
    Object.keys(record)
      .sort((left, right) => left.localeCompare(right))
      .map((key) => [key, record[key]])
  );

const buildLineStarts = (text) => {
  const starts = [0];
  for (let index = 0; index < text.length; index += 1) {
    if (text[index] === '\n') starts.push(index + 1);
  }
  return starts;
};

const offsetToLine = (lineStarts, offset) => {
  let low = 0;
  let high = lineStarts.length - 1;
  while (low <= high) {
    const mid = (low + high) >> 1;
    if (lineStarts[mid] <= offset) {
      low = mid + 1;
    } else {
      high = mid - 1;
    }
  }
  return high + 1;
};

const blankSpan = (src, start, end) => src.slice(start, end).replace(/[^\n]/g, ' ');

const consumeQuoted = (src, start) => {
  const quote = src[start];
  let index = start + 1;
  while (index < src.length) {
    if (src[index] === '\\') {
      index += 2;
      continue;
    }
    if (src[index] === '\n' && quote !== '`') return index;
    index += 1;
    if (src[index - 1] === quote) return index;
  }
  return index;
};

const consumeUrl = (src, start) => {
  if (
    (src[start] !== 'u' && src[start] !== 'U') ||
    src.slice(start, start + 4).toLowerCase() !== 'url(' ||
    (start > 0 && /[A-Za-z0-9_-]/.test(src[start - 1]))
  ) {
    return -1;
  }
  let index = start + 4;
  let depth = 1;
  while (index < src.length && depth > 0) {
    const ch = src[index];
    if (ch === '\\') {
      index += 2;
      continue;
    }
    if (ch === "'" || ch === '"') {
      index = consumeQuoted(src, index);
      continue;
    }
    if (ch === '/' && src[index + 1] === '*') {
      const close = src.indexOf('*/', index + 2);
      index = close === -1 ? src.length : close + 2;
      continue;
    }
    if (ch === '(') depth += 1;
    else if (ch === ')') depth -= 1;
    index += 1;
  }
  return index;
};

const consumeBalanced = (src, start, openChar, closeChar) => {
  let index = start + 1;
  let depth = 1;
  while (index < src.length && depth > 0) {
    const ch = src[index];
    if (ch === "'" || ch === '"') {
      index = consumeQuoted(src, index);
      continue;
    }
    if (ch === '#' && src[index + 1] === '{') {
      index = consumeBalanced(src, index + 1, '{', '}');
      continue;
    }
    if (ch === openChar) depth += 1;
    else if (ch === closeChar) depth -= 1;
    index += 1;
  }
  return index;
};

const consumeScriptBraces = (src, start) => {
  let index = start + 1;
  let depth = 1;
  while (index < src.length && depth > 0) {
    const ch = src[index];
    if (ch === "'" || ch === '"' || ch === '`') {
      index = consumeQuoted(src, index);
      continue;
    }
    if (ch === '{') depth += 1;
    else if (ch === '}') depth -= 1;
    index += 1;
  }
  return index;
};

export const stripComments = (source, { isScript = false, allowLineComments = true } = {}) => {
  let out = '';

  const scanTemplate = (start) => {
    out += '`';
    let index = start + 1;
    while (index < source.length) {
      const ch = source[index];
      if (ch === '\\') {
        out += source.slice(index, index + 2);
        index += 2;
        continue;
      }
      if (ch === '`') {
        out += '`';
        return index + 1;
      }
      if (ch === '$' && source[index + 1] === '{') {
        out += '${';
        index = scanCode(index + 2, true);
        continue;
      }
      out += ch;
      index += 1;
    }
    return index;
  };

  const scanCode = (start, inTemplateExpr = false) => {
    let index = start;
    let braceDepth = 0;

    while (index < source.length) {
      const ch = source[index];

      if (ch === '/' && source[index + 1] === '*') {
        const close = source.indexOf('*/', index + 2);
        const end = close === -1 ? source.length : close + 2;
        out += blankSpan(source, index, end);
        index = end;
        continue;
      }

      if (allowLineComments && ch === '/' && source[index + 1] === '/') {
        const nl = source.indexOf('\n', index + 2);
        const end = nl === -1 ? source.length : nl;
        out += blankSpan(source, index, end);
        index = end;
        continue;
      }

      if (!isScript) {
        const urlEnd = consumeUrl(source, index);
        if (urlEnd !== -1) {
          let u = index;
          while (u < urlEnd) {
            if (source[u] === "'" || source[u] === '"') {
              const qEnd = consumeQuoted(source, u);
              out += source.slice(u, qEnd);
              u = qEnd;
            } else if (source[u] === '/' && source[u + 1] === '*') {
              const close = source.indexOf('*/', u + 2);
              const cEnd = close === -1 ? urlEnd : Math.min(urlEnd, close + 2);
              out += blankSpan(source, u, cEnd);
              u = cEnd;
            } else {
              out += source[u];
              u += 1;
            }
          }
          index = urlEnd;
          continue;
        }
      }

      if (ch === "'" || ch === '"') {
        if (ch === "'" && isScript && index > 0 && /[A-Za-z0-9]/.test(source[index - 1])) {
          out += ch;
          index += 1;
          continue;
        }
        const end = consumeQuoted(source, index);
        out += source.slice(index, end);
        index = end;
        continue;
      }

      if (isScript && ch === '`') {
        index = scanTemplate(index);
        continue;
      }

      if (inTemplateExpr) {
        if (ch === '{') braceDepth += 1;
        else if (ch === '}') {
          if (braceDepth === 0) {
            out += '}';
            return index + 1;
          }
          braceDepth -= 1;
        }
      }

      out += ch;
      index += 1;
    }

    return index;
  };

  scanCode(0, false);
  return out;
};

const scanStyleSource = (cleaned) => {
  const declarations = [];
  const nonValueRanges = [];
  const length = cleaned.length;
  const blockPrefixes = [''];
  let index = 0;

  const pushDeclaration = (rawSlice, stmtStart, colonOffset, stmtEnd) => {
    const rawName = rawSlice.trim();
    if (!/^(?:--|\$)?[A-Za-z_][A-Za-z0-9_-]*(?:#\{[^}]*\}[A-Za-z0-9_-]*)*$/.test(rawName)) {
      return;
    }
    const firstNonWs = rawSlice.search(/\S/);
    const propOffset = stmtStart + (firstNonWs >= 0 ? firstNonWs : 0);
    const currentPrefix = blockPrefixes[blockPrefixes.length - 1];
    const propName =
      currentPrefix && !rawName.startsWith('--') && !rawName.startsWith('$')
        ? `${currentPrefix}-${rawName}`
        : rawName;
    declarations.push({
      propName,
      rawName,
      propOffset,
      valueText: cleaned.slice(colonOffset + 1, stmtEnd),
    });
  };

  while (index < length) {
    while (index < length && /\s/.test(cleaned[index])) index += 1;
    if (index >= length) break;

    if (cleaned[index] === '}') {
      if (blockPrefixes.length > 1) blockPrefixes.pop();
      index += 1;
      continue;
    }
    if (cleaned[index] === ';') {
      index += 1;
      continue;
    }

    const stmtStart = index;
    let colonOffset = -1;
    let isCustomProp = false;

    while (index < length) {
      const ch = cleaned[index];
      if (ch === "'" || ch === '"') {
        index = consumeQuoted(cleaned, index);
        continue;
      }
      if (ch === '#' && cleaned[index + 1] === '{') {
        index = consumeBalanced(cleaned, index + 1, '{', '}');
        continue;
      }
      if (ch === '(') {
        index = consumeBalanced(cleaned, index, '(', ')');
        continue;
      }
      if (ch === ':' && colonOffset === -1) {
        colonOffset = index;
        isCustomProp = /^--[A-Za-z0-9_-]+$/.test(cleaned.slice(stmtStart, colonOffset).trim());
        index += 1;
        continue;
      }
      if (ch === '{' && isCustomProp) {
        index = consumeBalanced(cleaned, index, '{', '}');
        continue;
      }
      if (ch === ';' || ch === '{' || ch === '}') {
        break;
      }
      index += 1;
    }

    const terminator = index < length ? cleaned[index] : '';
    const stmtEnd = index;

    if (terminator === '{') {
      let nextPrefix = '';
      if (colonOffset !== -1) {
        const rawSlice = cleaned.slice(stmtStart, colonOffset);
        const beforeColon = rawSlice.trim();
        const afterColon = cleaned.slice(colonOffset + 1, stmtEnd).trim();
        if (
          /^[A-Za-z][A-Za-z0-9-]*$/.test(beforeColon) &&
          (afterColon === '' || /\s/.test(cleaned[colonOffset + 1] ?? ''))
        ) {
          if (afterColon !== '') {
            pushDeclaration(rawSlice, stmtStart, colonOffset, stmtEnd);
          }
          const parentPrefix = blockPrefixes[blockPrefixes.length - 1];
          nextPrefix = parentPrefix ? `${parentPrefix}-${beforeColon}` : beforeColon;
          nonValueRanges.push([stmtStart, colonOffset + 1]);
        } else if (!cleaned.slice(stmtStart, stmtEnd).trimStart().startsWith('@')) {
          nonValueRanges.push([stmtStart, stmtEnd]);
        }
      } else if (!cleaned.slice(stmtStart, stmtEnd).trimStart().startsWith('@')) {
        nonValueRanges.push([stmtStart, stmtEnd]);
      }
      blockPrefixes.push(nextPrefix);
      index += 1;
      continue;
    }

    if (colonOffset !== -1) {
      pushDeclaration(cleaned.slice(stmtStart, colonOffset), stmtStart, colonOffset, stmtEnd);
    }

    if (terminator === ';') {
      index += 1;
    }
  }

  return { declarations, nonValueRanges };
};

const maskStyleSource = (cleaned, nonValueRanges) => {
  const chars = cleaned.split('');
  for (const [start, end] of nonValueRanges) {
    for (let index = start; index < end; index += 1) {
      if (chars[index] !== '\n') chars[index] = ' ';
    }
  }
  const withoutSelectors = chars.join('');
  let index = 0;
  while (index < withoutSelectors.length) {
    const ch = chars[index];
    if (ch === "'" || ch === '"') {
      const end = consumeQuoted(withoutSelectors, index);
      for (let cursor = index; cursor < end; cursor += 1) {
        if (chars[cursor] !== '\n') chars[cursor] = ' ';
      }
      index = end;
      continue;
    }
    const urlEnd = ch === ' ' ? -1 : consumeUrl(withoutSelectors, index);
    if (urlEnd !== -1) {
      for (let cursor = index; cursor < urlEnd; cursor += 1) {
        if (chars[cursor] !== '\n') chars[cursor] = ' ';
      }
      index = urlEnd;
      continue;
    }
    if (ch === '#' && chars[index + 1] === '{') {
      const interpEnd = consumeBalanced(withoutSelectors, index + 1, '{', '}');
      chars[index] = ' ';
      chars[index + 1] = ' ';
      if (interpEnd > index + 2 && withoutSelectors[interpEnd - 1] === '}') {
        chars[interpEnd - 1] = ' ';
      }
      index += 2;
      continue;
    }
    index += 1;
  }
  return chars.join('');
};

const walkScannedFiles = (dir) => {
  const results = [];
  const entries = readdirSync(dir, { withFileTypes: true }).sort((a, b) =>
    a.name.localeCompare(b.name)
  );

  for (const entry of entries) {
    const fullPath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (entry.name === '__tests__' || entry.name === 'node_modules' || entry.name === 'dist') {
        continue;
      }
      results.push(...walkScannedFiles(fullPath));
    } else if (entry.isFile()) {
      if (entry.name.endsWith('.d.ts')) continue;
      if (/\.(?:test|spec)\.[^.]+$/.test(entry.name)) continue;
      if (/\.(?:css|scss|ts|tsx)$/.test(entry.name)) {
        results.push(fullPath);
      }
    }
  }

  return results;
};

const splitRadiusShorthand = (rawValue) => {
  const value = rawValue.replace(/!important\b/gi, '').trim();
  const parts = [];
  let index = 0;
  let start = 0;

  while (index < value.length) {
    const ch = value[index];
    if (ch === "'" || ch === '"') {
      index = consumeQuoted(value, index);
    } else if (ch === '#' && value[index + 1] === '{') {
      index = consumeBalanced(value, index + 1, '{', '}');
    } else if (ch === '(') {
      index = consumeBalanced(value, index, '(', ')');
    } else if (/\s/.test(ch) || ch === '/') {
      if (index > start) parts.push(value.slice(start, index));
      index += 1;
      start = index;
    } else {
      index += 1;
    }
  }

  if (index > start) parts.push(value.slice(start, index));
  return parts;
};

const isAllowedRadiusPart = (part) =>
  ALLOWED_RADIUS_PARTS.has(part) ||
  /(?:var|calc|min|max|clamp)\s*\(/i.test(part) ||
  part.includes('$') ||
  part.includes('#{');

const isAllowedFontWeight = (rawValue) => {
  const value = rawValue.replace(/!important\b/gi, '').trim();
  return (
    !value ||
    ALLOWED_FONT_WEIGHTS.has(value) ||
    /^var\([\s\S]*\)$/.test(value) ||
    value.startsWith('$') ||
    value.startsWith('#{')
  );
};

const isAllowedFontShorthand = (rawValue) => {
  const value = rawValue.replace(/!important\b/gi, '').trim();
  if (!value) return true;
  let stripped = '';
  let index = 0;
  while (index < value.length) {
    const ch = value[index];
    if (ch === "'" || ch === '"') {
      index = consumeQuoted(value, index);
      stripped += ' ';
    } else if (ch === '#' && value[index + 1] === '{') {
      index = consumeBalanced(value, index + 1, '{', '}');
      stripped += ' ';
    } else if (ch === '(') {
      index = consumeBalanced(value, index, '(', ')');
      stripped += ' ';
    } else {
      stripped += ch;
      index += 1;
    }
  }
  const withoutLineHeight = stripped.replace(/\/\s*[^\s,;]+/g, ' ');
  for (const rawToken of withoutLineHeight.split(/[\s,]+/)) {
    const token = rawToken.trim().toLowerCase();
    if (!token) continue;
    if (token === 'bolder' || token === 'lighter') {
      return false;
    }
    if (/^\d+(?:\.\d+)?$/.test(token)) {
      const numeric = Number(token);
      if (numeric >= 1 && numeric <= 1000 && !ALLOWED_FONT_WEIGHTS.has(token)) {
        return false;
      }
    }
  }
  return true;
};

const resolveScanContext = (rootDir = defaultRootDir) => {
  const resolvedRoot = path.resolve(rootDir);
  if (resolvedRoot === defaultRootDir) {
    return { scanDir: resolvedRoot, baseDir: repoRoot };
  }
  const nestedWebSrc = path.join(resolvedRoot, 'apps/web/src');
  if (existsSync(nestedWebSrc)) {
    return { scanDir: nestedWebSrc, baseDir: resolvedRoot };
  }
  return { scanDir: resolvedRoot, baseDir: resolvedRoot };
};

export const collectFindings = (rootDir = defaultRootDir) => {
  const { scanDir, baseDir } = resolveScanContext(rootDir);
  const themesFilePath = path.join(scanDir, 'styles/themes.scss');
  const variablesFilePaths = new Set([
    path.join(scanDir, 'styles/variables.scss'),
    path.join(scanDir, 'variables.scss'),
  ]);
  const files = walkScannedFiles(scanDir);

  const definedVars = new Set();
  const varReferences = [];
  const occurrencesByRule = {
    'undefined-var': {},
    radius: {},
    'font-weight': {},
    'scss-radius-var': {},
    'color-literal': {},
    important: {},
  };
  const scssRadiusDefs = {};
  const scssRadiusDefinitionSites = [];

  const recordOccurrence = (rule, file, line, message) => {
    if (!occurrencesByRule[rule][file]) {
      occurrencesByRule[rule][file] = [];
    }
    occurrencesByRule[rule][file].push({ file, line, rule, message });
  };

  const collectVarRefs = (scanSource, rawCleaned, relPath, lineStarts) => {
    for (const match of scanSource.matchAll(/\bvar\(\s*(--[A-Za-z0-9_-]*)/g)) {
      const varName = match[1];
      const remainder = rawCleaned.slice(match.index + match[0].length);
      if (remainder.startsWith('${') || remainder.startsWith('#{') || remainder.startsWith('$')) {
        continue;
      }
      if (!varName || varName === '--') continue;
      varReferences.push({
        file: relPath,
        line: offsetToLine(lineStarts, match.index),
        name: varName,
      });
    }
  };

  for (const absolutePath of files) {
    const relPath = toPosixPath(path.relative(baseDir, absolutePath));
    const ext = path.extname(absolutePath);
    const isScript = ext === '.ts' || ext === '.tsx';
    const rawContent = readFileSync(absolutePath, 'utf8');
    const cleaned = stripComments(rawContent, {
      isScript,
      allowLineComments: ext !== '.css',
    });
    const lineStarts = buildLineStarts(cleaned);

    if (isScript) {
      collectVarRefs(cleaned, cleaned, relPath, lineStarts);
      for (const attrMatch of cleaned.matchAll(/(?<![A-Za-z0-9_.$])style\s*=\s*\{/g)) {
        const prefix = cleaned.slice(0, attrMatch.index);
        if (/(?:^|[\s;,(])(?:const|let|var|type|interface)\s+$/.test(prefix)) {
          continue;
        }
        const openBraceIndex = attrMatch.index + attrMatch[0].length - 1;
        const closeBraceIndex = consumeScriptBraces(cleaned, openBraceIndex);
        const exprSlice = cleaned.slice(openBraceIndex + 1, closeBraceIndex - 1);
        for (const keyMatch of exprSlice.matchAll(
          /(?:\[\s*['"`](--[A-Za-z0-9_-]+)['"`]\s*\]|['"`](--[A-Za-z0-9_-]+)['"`])\s*:/g
        )) {
          definedVars.add(keyMatch[1] ?? keyMatch[2]);
        }
      }
      for (const match of cleaned.matchAll(
        /\.setProperty\s*\(\s*['"`](--[A-Za-z0-9_-]+)['"`]/g
      )) {
        definedVars.add(match[1]);
      }
      continue;
    }

    const isThemesFile = absolutePath === themesFilePath;
    const isVariablesFile = variablesFilePaths.has(absolutePath);
    const { declarations, nonValueRanges } = scanStyleSource(cleaned);
    const maskedStyle = maskStyleSource(cleaned, nonValueRanges);

    collectVarRefs(maskedStyle, cleaned, relPath, lineStarts);

    for (const decl of declarations) {
      const declLine = offsetToLine(lineStarts, decl.propOffset);
      const compactValue = decl.valueText.replace(/\s+/g, ' ').trim();
      const propLower = decl.propName.toLowerCase();

      if (/^--[A-Za-z0-9_-]+$/.test(decl.rawName)) {
        definedVars.add(decl.rawName);
      }

      if (isVariablesFile && /^\$radius-[A-Za-z0-9_-]+$/.test(decl.rawName)) {
        scssRadiusDefs[decl.rawName] = compactValue;
        scssRadiusDefinitionSites.push({
          file: relPath,
          line: declLine,
          name: decl.rawName,
          value: compactValue,
        });
      }

      if (
        /^border(?:-[a-z]+-[a-z]+)?-radius$/i.test(decl.propName) ||
        /^--[A-Za-z0-9_-]*radius[A-Za-z0-9_-]*$/i.test(decl.rawName)
      ) {
        const parts = splitRadiusShorthand(decl.valueText);
        if (parts.length > 0 && !parts.every(isAllowedRadiusPart)) {
          recordOccurrence(
            'radius',
            relPath,
            declLine,
            `disallowed radius "${decl.propName}: ${compactValue}"`
          );
        }
      }

      if (
        (propLower === 'font-weight' && !isAllowedFontWeight(decl.valueText)) ||
        (propLower === 'font' && !isAllowedFontShorthand(decl.valueText))
      ) {
        recordOccurrence(
          'font-weight',
          relPath,
          declLine,
          `disallowed font-weight "${compactValue}"`
        );
      }
    }

    if (!isThemesFile) {
      for (const match of maskedStyle.matchAll(
        /(?<![A-Za-z0-9_$-])#(?:[0-9a-fA-F]{3,4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})\b/g
      )) {
        recordOccurrence(
          'color-literal',
          relPath,
          offsetToLine(lineStarts, match.index),
          `colour literal "${match[0]}" outside styles/themes.scss`
        );
      }
      for (const match of maskedStyle.matchAll(
        /(?<![A-Za-z0-9_$-])(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch|color)\s*\(/gi
      )) {
        recordOccurrence(
          'color-literal',
          relPath,
          offsetToLine(lineStarts, match.index),
          `colour literal "${match[0]})" outside styles/themes.scss`
        );
      }
    }

    for (const match of maskedStyle.matchAll(/\$radius-[A-Za-z0-9_-]+/g)) {
      recordOccurrence(
        'scss-radius-var',
        relPath,
        offsetToLine(lineStarts, match.index),
        `SCSS radius variable "${match[0]}"`
      );
    }

    for (const match of maskedStyle.matchAll(/!important\b/gi)) {
      recordOccurrence(
        'important',
        relPath,
        offsetToLine(lineStarts, match.index),
        `avoid "${match[0]}"`
      );
    }
  }

  const undefinedVarCounts = {};
  for (const ref of varReferences) {
    if (definedVars.has(ref.name)) continue;
    if (!occurrencesByRule['undefined-var'][ref.file]) {
      occurrencesByRule['undefined-var'][ref.file] = {};
    }
    if (!occurrencesByRule['undefined-var'][ref.file][ref.name]) {
      occurrencesByRule['undefined-var'][ref.file][ref.name] = [];
    }
    occurrencesByRule['undefined-var'][ref.file][ref.name].push({
      file: ref.file,
      line: ref.line,
      rule: 'undefined-var',
      name: ref.name,
      message: `undefined CSS variable "${ref.name}"`,
    });

    if (!undefinedVarCounts[ref.file]) {
      undefinedVarCounts[ref.file] = {};
    }
    undefinedVarCounts[ref.file][ref.name] = (undefinedVarCounts[ref.file][ref.name] ?? 0) + 1;
  }

  const sortedUndefinedVarRules = {};
  for (const file of Object.keys(undefinedVarCounts).sort((a, b) => a.localeCompare(b))) {
    sortedUndefinedVarRules[file] = sortObjectKeys(undefinedVarCounts[file]);
  }

  const rules = {
    'undefined-var': sortedUndefinedVarRules,
  };

  for (const rule of RATCHET_FILE_RULES) {
    const fileCounts = {};
    for (const [file, items] of Object.entries(occurrencesByRule[rule])) {
      if (items.length > 0) {
        fileCounts[file] = items.length;
      }
    }
    rules[rule] = sortObjectKeys(fileCounts);
  }

  return {
    filesScanned: files.length,
    rules,
    scssRadiusDefinitions: sortObjectKeys(scssRadiusDefs),
    scssRadiusDefinitionSites,
    occurrencesByRule,
  };
};

export const buildBaselineFromFindings = (findings) => {
  const rules = {
    'undefined-var': {},
  };

  for (const file of Object.keys(findings.rules['undefined-var'] ?? {}).sort((a, b) =>
    a.localeCompare(b)
  )) {
    rules['undefined-var'][file] = sortObjectKeys(findings.rules['undefined-var'][file]);
  }

  for (const rule of RATCHET_FILE_RULES) {
    rules[rule] = sortObjectKeys(findings.rules[rule] ?? {});
  }

  return {
    version: BASELINE_VERSION,
    rules,
    scssRadiusDefinitions: sortObjectKeys(findings.scssRadiusDefinitions ?? {}),
  };
};

const isPlainObject = (value) =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

export const validateBaseline = (baseline) => {
  const errors = [];
  if (!isPlainObject(baseline)) {
    return ['Baseline must be a JSON object'];
  }
  if (baseline.version !== BASELINE_VERSION) {
    errors.push(
      `Unsupported baseline version: expected ${BASELINE_VERSION}, got ${JSON.stringify(baseline.version)}`
    );
  }
  if (!isPlainObject(baseline.rules)) {
    errors.push('Baseline is missing a valid "rules" object');
  } else {
    for (const ruleKey of ALL_RULE_KEYS) {
      const ruleEntry = baseline.rules[ruleKey];
      if (!isPlainObject(ruleEntry)) {
        errors.push(`Baseline "rules.${ruleKey}" must be an object`);
        continue;
      }
      if (ruleKey === 'undefined-var') {
        for (const [file, varMap] of Object.entries(ruleEntry)) {
          if (!isPlainObject(varMap)) {
            errors.push(`Baseline "rules.undefined-var[${file}]" must be an object`);
            continue;
          }
          for (const [varName, count] of Object.entries(varMap)) {
            if (!Number.isInteger(count) || count < 0) {
              errors.push(
                `Baseline "rules.undefined-var[${file}][${varName}]" must be a non-negative integer`
              );
            }
          }
        }
      } else {
        for (const [file, count] of Object.entries(ruleEntry)) {
          if (!Number.isInteger(count) || count < 0) {
            errors.push(`Baseline "rules.${ruleKey}[${file}]" must be a non-negative integer`);
          }
        }
      }
    }
  }
  if (!isPlainObject(baseline.scssRadiusDefinitions)) {
    errors.push('Baseline "scssRadiusDefinitions" must be a "{ name: rawValue }" object');
  } else {
    for (const [name, rawValue] of Object.entries(baseline.scssRadiusDefinitions)) {
      if (!/^\$radius-[A-Za-z0-9_-]+$/.test(name)) {
        errors.push(`Invalid SCSS radius variable name in baseline: "${name}"`);
      }
      if (typeof rawValue !== 'string' || rawValue.trim() === '') {
        errors.push(
          `Baseline "scssRadiusDefinitions[${name}]" must be a non-empty raw value string`
        );
      }
    }
  }
  return errors;
};

export const compareToBaseline = (findings, baseline = {}) => {
  const failures = [];
  const hardFailures = [];
  const tightenNotices = [];
  const increases = [];
  const schemaErrors = validateBaseline(baseline);

  if (schemaErrors.length > 0) {
    for (const message of schemaErrors) {
      const hardFailure = {
        file: 'bin/ci/design-token-baseline.json',
        line: 1,
        rule: 'baseline-schema',
        message,
      };
      failures.push(hardFailure);
      hardFailures.push(hardFailure);
    }
    return {
      passed: false,
      failures,
      hardFailures,
      tightenNotices,
      increases,
    };
  }

  const baselineRules = baseline.rules;
  const currentUndefined = findings.rules?.['undefined-var'] ?? {};
  const baselineUndefined = baselineRules['undefined-var'] ?? {};
  const allUndefinedFiles = new Set([
    ...Object.keys(currentUndefined),
    ...Object.keys(baselineUndefined),
  ]);

  for (const file of [...allUndefinedFiles].sort((a, b) => a.localeCompare(b))) {
    const currentVars = currentUndefined[file] ?? {};
    const baselineVars = baselineUndefined[file] ?? {};
    const allVarNames = new Set([...Object.keys(currentVars), ...Object.keys(baselineVars)]);

    for (const varName of [...allVarNames].sort((a, b) => a.localeCompare(b))) {
      const currentCount = currentVars[varName] ?? 0;
      const baselineCount = baselineVars[varName] ?? 0;

      if (currentCount > baselineCount) {
        increases.push({
          rule: 'undefined-var',
          file,
          name: varName,
          current: currentCount,
          baseline: baselineCount,
        });
        const sites = findings.occurrencesByRule?.['undefined-var']?.[file]?.[varName] ?? [
          {
            file,
            line: 1,
            rule: 'undefined-var',
            name: varName,
            message: `undefined CSS variable "${varName}"`,
          },
        ];
        for (const site of sites) {
          failures.push({
            file: site.file,
            line: site.line,
            rule: 'undefined-var',
            name: varName,
            current: currentCount,
            baseline: baselineCount,
            message: `${site.message} (count ${currentCount} > baseline ${baselineCount})`,
          });
        }
      } else if (currentCount < baselineCount) {
        tightenNotices.push({
          rule: 'undefined-var',
          file,
          name: varName,
          current: currentCount,
          baseline: baselineCount,
          message: `undefined-var ${file} (${varName}): ${currentCount} < baseline ${baselineCount}`,
        });
      }
    }
  }

  for (const rule of RATCHET_FILE_RULES) {
    const currentFiles = findings.rules?.[rule] ?? {};
    const baselineFiles = baselineRules[rule] ?? {};
    const allFiles = new Set([...Object.keys(currentFiles), ...Object.keys(baselineFiles)]);

    for (const file of [...allFiles].sort((a, b) => a.localeCompare(b))) {
      const currentCount = currentFiles[file] ?? 0;
      const baselineCount = baselineFiles[file] ?? 0;

      if (currentCount > baselineCount) {
        increases.push({
          rule,
          file,
          current: currentCount,
          baseline: baselineCount,
        });
        const sites = findings.occurrencesByRule?.[rule]?.[file] ?? [
          {
            file,
            line: 1,
            rule,
            message: `${rule} count ${currentCount} exceeds baseline ${baselineCount}`,
          },
        ];
        for (const site of sites) {
          failures.push({
            file: site.file,
            line: site.line,
            rule,
            current: currentCount,
            baseline: baselineCount,
            message: `${site.message} (file count ${currentCount} > baseline ${baselineCount})`,
          });
        }
      } else if (currentCount < baselineCount) {
        tightenNotices.push({
          rule,
          file,
          current: currentCount,
          baseline: baselineCount,
          message: `${rule} ${file}: ${currentCount} < baseline ${baselineCount}`,
        });
      }
    }
  }

  const allowedDefs = baseline.scssRadiusDefinitions;
  const currentDefs = findings.scssRadiusDefinitions ?? {};
  for (const [defName, currentValue] of Object.entries(currentDefs)) {
    const site = (findings.scssRadiusDefinitionSites ?? []).find(
      (candidate) => candidate.name === defName
    );
    const file = site?.file ?? 'apps/web/src/styles/variables.scss';
    const line = site?.line ?? 1;
    if (!Object.prototype.hasOwnProperty.call(allowedDefs, defName)) {
      const hardFailure = {
        file,
        line,
        rule: 'scss-radius-var',
        name: defName,
        current: currentValue,
        baseline: null,
        message: `new SCSS radius variable definition "${defName}" is not allowed in variables.scss`,
      };
      failures.push(hardFailure);
      hardFailures.push(hardFailure);
    } else if (allowedDefs[defName] !== currentValue) {
      const hardFailure = {
        file,
        line,
        rule: 'scss-radius-var',
        name: defName,
        current: currentValue,
        baseline: allowedDefs[defName],
        message: `SCSS radius variable "${defName}" value changed from "${allowedDefs[defName]}" to "${currentValue}" in variables.scss`,
      };
      failures.push(hardFailure);
      hardFailures.push(hardFailure);
    }
  }
  for (const defName of Object.keys(allowedDefs)) {
    if (!Object.prototype.hasOwnProperty.call(currentDefs, defName)) {
      tightenNotices.push({
        rule: 'scss-radius-var',
        file: 'apps/web/src/styles/variables.scss',
        name: defName,
        current: 0,
        baseline: 1,
        message: `scssRadiusDefinitions (${defName}): removed from variables.scss`,
      });
    }
  }

  return {
    passed: failures.length === 0,
    failures,
    hardFailures,
    tightenNotices,
    increases,
  };
};

const summarizeUndefinedDebt = (undefinedRuleMap = {}) => {
  let sites = 0;
  const names = new Set();
  const files = Object.keys(undefinedRuleMap);

  for (const file of files) {
    for (const [name, count] of Object.entries(undefinedRuleMap[file])) {
      if (count > 0) {
        sites += count;
        names.add(name);
      }
    }
  }

  return { sites, namesCount: names.size, filesCount: files.length };
};

const parseCliArgs = (argv) => {
  let rootDir = defaultRootDir;
  let baselinePath = defaultBaselinePath;
  let updateBaseline = false;
  let allowIncrease = false;
  let initBaseline = false;

  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (arg === '--root') {
      rootDir = argv[index + 1];
      index += 1;
    } else if (arg === '--baseline') {
      baselinePath = argv[index + 1];
      index += 1;
    } else if (arg === '--update-baseline') {
      updateBaseline = true;
    } else if (arg === '--allow-increase') {
      allowIncrease = true;
    } else if (arg === '--init') {
      initBaseline = true;
    } else {
      throw new Error(`Unknown argument: ${arg}`);
    }
  }

  return {
    rootDir: path.resolve(rootDir),
    baselinePath: path.resolve(baselinePath),
    updateBaseline,
    allowIncrease,
    initBaseline,
  };
};

export const runCli = (argv = process.argv.slice(2)) => {
  let options;
  try {
    options = parseCliArgs(argv);
  } catch (error) {
    console.error(error.message);
    return 1;
  }

  const findings = collectFindings(options.rootDir);

  if (options.initBaseline) {
    if (options.updateBaseline) {
      console.error('Cannot combine --init with --update-baseline');
      return 1;
    }
    if (existsSync(options.baselinePath)) {
      console.error(
        `Refusing to overwrite existing design token baseline with --init: ${options.baselinePath}`
      );
      return 1;
    }
    const nextBaseline = buildBaselineFromFindings(findings);
    writeFileSync(options.baselinePath, `${JSON.stringify(nextBaseline, null, 2)}\n`, 'utf8');
    console.log(
      `Initialized design token baseline: ${toPosixPath(path.relative(repoRoot, options.baselinePath))}`
    );
    return 0;
  }

  if (!existsSync(options.baselinePath)) {
    console.error(
      `Missing design token baseline file: ${options.baselinePath} (use --init for first-time generation)`
    );
    return 1;
  }

  let existingBaseline;
  try {
    existingBaseline = JSON.parse(readFileSync(options.baselinePath, 'utf8'));
  } catch (error) {
    console.error(
      `Failed to read design token baseline file ${options.baselinePath}: ${error.message}`
    );
    return 1;
  }

  const schemaErrors = validateBaseline(existingBaseline);
  if (schemaErrors.length > 0) {
    console.error(`Invalid design token baseline schema in ${options.baselinePath}:`);
    for (const err of schemaErrors) {
      console.error(`  ${err}`);
    }
    return 1;
  }

  if (options.updateBaseline) {
    const comparison = compareToBaseline(findings, existingBaseline);
    if (comparison.hardFailures.length > 0) {
      for (const failure of comparison.hardFailures) {
        console.error(`${failure.file}:${failure.line} ${failure.rule} ${failure.message}`);
      }
      return 1;
    }
    if (!options.allowIncrease && comparison.increases.length > 0) {
      console.error(
        'Refusing to update baseline because violation counts increased (pass --allow-increase if intentional):'
      );
      for (const inc of comparison.increases) {
        const label = inc.name ? `${inc.file} (${inc.name})` : inc.file;
        console.error(`  ${inc.rule} ${label}: ${inc.baseline} -> ${inc.current}`);
      }
      return 1;
    }

    const nextBaseline = buildBaselineFromFindings(findings);
    writeFileSync(options.baselinePath, `${JSON.stringify(nextBaseline, null, 2)}\n`, 'utf8');
    console.log(
      `Updated design token baseline: ${toPosixPath(path.relative(repoRoot, options.baselinePath))}`
    );
    return 0;
  }
  const comparison = compareToBaseline(findings, existingBaseline);

  if (comparison.failures.length > 0) {
    for (const failure of comparison.failures) {
      console.error(`${failure.file}:${failure.line} ${failure.rule} ${failure.message}`);
    }
    console.error(
      'Remediation: use theme tokens from apps/web/src/styles/themes.scss, concentric radius scale tokens, and 400-700 font weights (see docs/development/ui-design-system.md).'
    );
  }

  if (comparison.tightenNotices.length > 0) {
    console.log(
      `Notice: ${comparison.tightenNotices.length} baseline entry(s) can be tightened with --update-baseline:`
    );
    for (const notice of comparison.tightenNotices) {
      console.log(`  ${notice.message}`);
    }
  }

  const debt = summarizeUndefinedDebt(findings.rules['undefined-var']);
  console.log(
    `Scanned ${findings.filesScanned} files in ${toPosixPath(path.relative(repoRoot, options.rootDir)) || '.'}`
  );
  console.log(
    `R1 undefined-var — known undefined-variable debt: ${debt.sites} sites / ${debt.namesCount} names across ${debt.filesCount} files (target 0)`
  );
  console.log('Rule summary (current count / files with occurrences):');
  for (const rule of RATCHET_FILE_RULES) {
    const fileMap = findings.rules[rule] ?? {};
    const filesCount = Object.keys(fileMap).length;
    const totalCount = Object.values(fileMap).reduce((sum, count) => sum + count, 0);
    console.log(`  ${rule.padEnd(16)} ${String(totalCount).padStart(5)} across ${filesCount} files`);
  }

  return comparison.passed ? 0 : 1;
};

const entryPoint = process.argv[1] ? pathToFileURL(path.resolve(process.argv[1])).href : '';
if (entryPoint === import.meta.url) {
  process.exitCode = runCli();
}
