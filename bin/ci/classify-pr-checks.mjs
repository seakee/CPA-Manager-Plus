import { existsSync, lstatSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const CHECK_NAMES = ['frontend', 'manager_server', 'windows_sqlite', 'native_control', 'docker'];

const FORBIDDEN_INVISIBLE_CODE_POINTS = new Set([
  0x200b, 0x200c, 0x200d, 0x200e, 0x200f, 0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2060, 0x2066,
  0x2067, 0x2068, 0x2069, 0xfeff,
]);

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');

const normalizePath = (filePath) => filePath.trim().replaceAll('\\', '/').replace(/^\.\//, '');

const normalizeChangedFiles = (changedFiles) => [
  ...new Set(changedFiles.map(normalizePath).filter(Boolean)),
];

const startsWithPath = (filePath, directory) => filePath.startsWith(`${directory}/`);

const allChecks = (enabled) =>
  Object.fromEntries(CHECK_NAMES.map((checkName) => [checkName, enabled]));

const triggersAllChecks = (filePath) =>
  startsWithPath(filePath, '.github/workflows') ||
  filePath === 'bin/ci/classify-pr-checks.mjs' ||
  filePath === 'tests/prCheckClassifier.test.mjs';

const triggersFrontend = (filePath) =>
  startsWithPath(filePath, 'apps/web') ||
  startsWithPath(filePath, 'apps/docs') ||
  startsWithPath(filePath, 'tests') ||
  filePath === 'README.md' ||
  filePath === 'README_CN.md' ||
  filePath === 'package.json' ||
  filePath === 'package-lock.json' ||
  filePath === 'eslint.config.js' ||
  filePath === 'bin/install-cpamp.sh' ||
  filePath === 'bin/release/package-native.sh' ||
  filePath === 'bin/release/check-web-demo-isolation.mjs';

const triggersManagerServer = (filePath) =>
  startsWithPath(filePath, 'apps/manager-server') || filePath === 'bin/release/package-native.sh';

const triggersNativeControl = (filePath) =>
  startsWithPath(filePath, 'bin/native') ||
  filePath === 'bin/release/package-native.sh' ||
  filePath === 'tests/nativeControlScripts.test.mjs' ||
  filePath === 'package.json' ||
  filePath === 'package-lock.json';

const triggersDocker = (filePath) =>
  startsWithPath(filePath, 'apps/web') ||
  startsWithPath(filePath, 'apps/manager-server') ||
  filePath === 'Dockerfile.manager-server' ||
  filePath === 'docker-compose.manager.yml' ||
  filePath === '.dockerignore' ||
  filePath === 'package.json' ||
  filePath === 'package-lock.json';

export const classifyChangedFiles = (changedFiles) => {
  const files = normalizeChangedFiles(changedFiles);
  if (files.length === 0 || files.some(triggersAllChecks)) return allChecks(true);

  const frontend = files.some(triggersFrontend);
  const managerServer = files.some(triggersManagerServer);

  return {
    frontend,
    manager_server: managerServer,
    windows_sqlite: managerServer,
    native_control: files.some(triggersNativeControl),
    docker: files.some(triggersDocker),
  };
};

export const findForbiddenUnicode = (text) => {
  const hits = [];

  for (let index = 0; index < text.length; index += 1) {
    const codePoint = text.codePointAt(index);
    if (codePoint === undefined) continue;

    if (FORBIDDEN_INVISIBLE_CODE_POINTS.has(codePoint)) {
      hits.push(`U+${codePoint.toString(16).toUpperCase().padStart(4, '0')}`);
    }

    if (codePoint > 0xffff) index += 1;
  }

  return hits;
};

export const scanChangedTextFiles = (changedFiles) => {
  const violations = [];

  for (const filePath of normalizeChangedFiles(changedFiles)) {
    const absolutePath = path.resolve(repoRoot, filePath);
    if (!absolutePath.startsWith(`${repoRoot}${path.sep}`)) {
      violations.push(`${filePath} resolves outside the repository`);
      continue;
    }
    if (!existsSync(absolutePath)) continue;
    if (!lstatSync(absolutePath).isFile()) continue;

    const fileBuffer = readFileSync(absolutePath);
    if (fileBuffer.includes(0)) continue;

    for (const codePoint of findForbiddenUnicode(fileBuffer.toString('utf8'))) {
      violations.push(`${filePath} contains ${codePoint}`);
    }
  }

  return violations;
};

const runCli = () => {
  const changedFiles = readFileSync(0, 'utf8').split(/\r?\n/);
  const violations = scanChangedTextFiles(changedFiles);

  if (violations.length > 0) {
    console.error('Changed text files contain forbidden invisible Unicode:');
    for (const violation of violations) console.error(`- ${violation}`);
    process.exitCode = 1;
    return;
  }

  const classification = classifyChangedFiles(changedFiles);
  for (const checkName of CHECK_NAMES) {
    console.log(`${checkName}=${classification[checkName]}`);
  }
};

const entryPoint = process.argv[1] ? pathToFileURL(path.resolve(process.argv[1])).href : '';
if (entryPoint === import.meta.url) runCli();
