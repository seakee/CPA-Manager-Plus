import { execFileSync } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { generateReleaseInfo } from './generate-release-info.mjs';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const repositoryUrl = 'https://github.com/seakee/CPA-Manager-Plus';
const maximumTelegramCharacters = 3500;
const prereleaseIdentifier = String.raw`(?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*)`;
const releaseTagPattern = new RegExp(
  String.raw`^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-(${prereleaseIdentifier}(?:\.${prereleaseIdentifier})*))?$`
);
const htmlEntityPattern = /&(?:amp|lt|gt|quot|#[0-9]+|#[xX][0-9A-Fa-f]+);/y;

const runGit = (args) =>
  execFileSync('git', args, {
    cwd: repoRoot,
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
  }).trim();

const releasePaths = (tag) => ({
  chinese: `docs/release-notes/${tag}-zh.md`,
  english: `docs/release-notes/${tag}-en.md`,
  telegram: `docs/release-posts/${tag}-telegram.html`,
});

const expectedLanguageLink = (tag, language) =>
  `${repositoryUrl}/blob/${tag}/docs/release-notes/${tag}-${language}.md`;

const fail = (message) => {
  throw new Error(message);
};

export const parseReleaseTag = (tag) => {
  if (typeof tag !== 'string' || !releaseTagPattern.test(tag)) {
    fail(`Release tag must match v<major>.<minor>.<patch>[-<prerelease>]: ${tag || '<empty>'}`);
  }

  return {
    tag,
    prerelease: releaseTagPattern.exec(tag)?.[4] !== undefined,
  };
};

const validateEscapedHtmlText = (text, context) => {
  for (let index = 0; index < text.length; index += 1) {
    const character = text[index];
    if (character === '<' || character === '>') {
      fail(`${context} contains an unescaped ${character}`);
    }
    if (character !== '&') continue;

    htmlEntityPattern.lastIndex = index;
    const entity = htmlEntityPattern.exec(text);
    if (!entity) fail(`${context} contains an invalid or unescaped HTML entity`);
    index = htmlEntityPattern.lastIndex - 1;
  }
};

export const validateTelegramHtml = (body) => {
  if (typeof body !== 'string' || body.trim() === '') fail('Telegram release post is empty');
  if (Array.from(body).length > maximumTelegramCharacters) {
    fail(`Telegram release post exceeds ${maximumTelegramCharacters} characters`);
  }
  if (body.includes('发布截图') || /CPA-Manager-Plus\s*\[/.test(body)) {
    fail('Telegram release post contains Markdown-only release sections');
  }

  const stack = [];
  let offset = 0;
  while (offset < body.length) {
    if (body[offset] !== '<') {
      const nextTag = body.indexOf('<', offset);
      const textEnd = nextTag === -1 ? body.length : nextTag;
      validateEscapedHtmlText(body.slice(offset, textEnd), 'Telegram release text');
      offset = textEnd;
      continue;
    }

    const end = body.indexOf('>', offset + 1);
    if (end === -1) fail('Telegram release post contains an unterminated HTML tag');
    const token = body.slice(offset, end + 1);

    if (token === '<b>' || token === '<i>' || token === '<code>') {
      stack.push(token.slice(1, -1));
    } else if (token === '</b>' || token === '</i>' || token === '</code>') {
      const tag = token.slice(2, -1);
      if (stack.pop() !== tag) fail(`Telegram release post has unbalanced </${tag}>`);
    } else if (token.startsWith('<a ') && token.endsWith('>')) {
      const match = /^<a href="([^"]+)">$/.exec(token);
      if (!match) fail('Telegram <a> tags must contain exactly one double-quoted href');
      validateEscapedHtmlText(match[1], 'Telegram <a href>');
      let url;
      try {
        url = new URL(match[1].replaceAll('&amp;', '&'));
      } catch {
        fail('Telegram <a href> values must be valid absolute URLs');
      }
      if (url.protocol !== 'https:' || url.hostname === '') {
        fail('Telegram <a href> values must be absolute HTTPS URLs');
      }
      stack.push('a');
    } else if (token === '</a>') {
      if (stack.pop() !== 'a') fail('Telegram release post has unbalanced </a>');
    } else {
      fail(`Telegram release post contains unsupported HTML: ${token}`);
    }

    offset = end + 1;
  }

  if (stack.length > 0) fail(`Telegram release post has unclosed <${stack.at(-1)}> tag`);
  return { characters: Array.from(body).length };
};

export const validateReleaseNotes = ({ tag, chinese, english }) => {
  parseReleaseTag(tag);
  if (typeof chinese !== 'string' || chinese.trim() === '') fail('Chinese release notes are empty');
  if (typeof english !== 'string' || english.trim() === '') fail('English release notes are empty');

  const englishLink = expectedLanguageLink(tag, 'en');
  const chineseLink = expectedLanguageLink(tag, 'zh');
  if (!chinese.includes(englishLink)) fail(`Chinese release notes must link to ${englishLink}`);
  if (!english.includes(chineseLink)) fail(`English release notes must link to ${chineseLink}`);
  if (/\]\(\.\/?[^)]*release-notes/.test(chinese) || /\]\(\.\/?[^)]*release-notes/.test(english)) {
    fail('Release note language links must be tag-pinned GitHub blob URLs');
  }

  return {
    chineseCharacters: Array.from(chinese).length,
    englishCharacters: Array.from(english).length,
  };
};

export const validateReleaseContent = ({
  tag,
  readFile = (filePath) => readFileSync(filePath, 'utf8'),
  fileExists = (filePath) => existsSync(filePath),
}) => {
  parseReleaseTag(tag);
  const paths = releasePaths(tag);
  const missing = Object.values(paths).filter(
    (relativePath) => !fileExists(path.resolve(repoRoot, relativePath))
  );
  if (missing.length > 0) fail(`Missing required release files: ${missing.join(', ')}`);

  const chinese = readFile(path.resolve(repoRoot, paths.chinese));
  const english = readFile(path.resolve(repoRoot, paths.english));
  const telegram = readFile(path.resolve(repoRoot, paths.telegram));
  generateReleaseInfo(tag, '0'.repeat(40), chinese);
  return {
    paths,
    notes: validateReleaseNotes({ tag, chinese, english }),
    telegram: validateTelegramHtml(telegram),
  };
};

const normalizeChangedPath = (filePath) =>
  filePath.replace(/\r$/, '').replaceAll('\\', '/').replace(/^\.\//, '');

const releaseTagFromPath = (filePath) => {
  const normalizedPath = normalizeChangedPath(filePath);
  const noteMatch = /^docs\/release-notes\/(.+)-(?:zh|en)\.md$/.exec(normalizedPath);
  if (noteMatch) return noteMatch[1];
  const postMatch = /^docs\/release-posts\/(.+)-telegram\.html$/.exec(normalizedPath);
  if (postMatch) return postMatch[1];
  if (
    normalizedPath.startsWith('docs/release-notes/') ||
    normalizedPath.startsWith('docs/release-posts/')
  ) {
    fail(`Unexpected release content path: ${normalizedPath}`);
  }
  return null;
};

export const validateChangedReleaseContent = ({ changedFiles, readFile, fileExists }) => {
  const tags = [
    ...new Set(
      changedFiles
        .map(releaseTagFromPath)
        .filter((tag) => tag !== null)
        .map((tag) => parseReleaseTag(tag).tag)
    ),
  ].sort();

  return {
    tags,
    releases: tags.map((tag) =>
      validateReleaseContent({
        tag,
        ...(readFile ? { readFile } : {}),
        ...(fileExists ? { fileExists } : {}),
      })
    ),
  };
};

const resolveCommit = (git, ref) => git(['rev-parse', '--verify', `${ref}^{commit}`]);
const resolveTree = (git, ref) => git(['rev-parse', '--verify', `${ref}^{tree}`]);

const commitParents = (git, sha) => {
  const values = git(['rev-list', '--parents', '-n', '1', sha]).split(/\s+/);
  if (values.shift() !== sha) fail(`Git did not return the expected commit for ${sha}`);
  return values;
};

const changedNames = (git, base, commit) =>
  git(['diff', '--name-status', '--no-renames', base, commit])
    .split('\n')
    .filter(Boolean)
    .map((line) => {
      const [status, ...fileParts] = line.split('\t');
      return { status, path: fileParts.join('\t') };
    });

export const validateReleaseTopology = ({
  tag,
  sha,
  mainRef = 'origin/main',
  devRef = 'origin/dev',
  requireTagRef = true,
  git = runGit,
}) => {
  parseReleaseTag(tag);
  const candidateSha = sha || resolveCommit(git, 'HEAD');
  const mainSha = resolveCommit(git, mainRef);
  const devSha = resolveCommit(git, devRef);

  if (requireTagRef && resolveCommit(git, `refs/tags/${tag}`) !== candidateSha) {
    fail(`Tag ${tag} does not point to the candidate release commit ${candidateSha}`);
  }
  if (candidateSha !== mainSha)
    fail(`Candidate ${candidateSha} is not the current ${mainRef} ${mainSha}`);

  const mainParents = commitParents(git, mainSha);
  if (mainParents.length !== 2 || mainParents[1] !== devSha) {
    fail(`Current main must be a promotion merge whose second parent is ${devRef}`);
  }

  const mainTree = resolveTree(git, mainSha);
  const devTree = resolveTree(git, devSha);
  if (mainTree !== devTree) {
    fail(`Current main tree must exactly match the promoted ${devRef} tree`);
  }

  const devParents = commitParents(git, devSha);
  if (devParents.length !== 2) fail(`Current dev ${devSha} must be the release PR merge commit`);

  const expected = releasePaths(tag);
  const expectedFiles = new Set(Object.values(expected));
  const changes = changedNames(git, devParents[0], devSha);
  const changedFiles = new Set(changes.map(({ path: filePath }) => filePath));
  const invalidChanges = changes.filter(
    ({ status, path: filePath }) => status !== 'A' || !expectedFiles.has(filePath)
  );
  const missingChanges = [...expectedFiles].filter((filePath) => !changedFiles.has(filePath));
  if (
    invalidChanges.length > 0 ||
    missingChanges.length > 0 ||
    changes.length !== expectedFiles.size
  ) {
    fail('The current dev tip is not the exact release PR merge for this tag');
  }

  return {
    tag,
    candidateSha,
    mainSha,
    devSha,
    mainTree,
    devTree,
    mainParents,
    devParents,
    changes,
  };
};

const parseArguments = (argv) => {
  const options = { contentOnly: false, dryRun: false, changedContent: false, null: false };
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (argument === '--content-only') options.contentOnly = true;
    else if (argument === '--dry-run') options.dryRun = true;
    else if (argument === '--changed-content') options.changedContent = true;
    else if (argument === '--null') options.null = true;
    else if (argument.startsWith('--')) {
      const key = argument.slice(2).replace(/-([a-z])/g, (_, character) => character.toUpperCase());
      options[key] = argv[++index];
    } else fail(`Unknown release validation argument: ${argument}`);
  }
  return options;
};

const runCli = () => {
  try {
    const options = parseArguments(process.argv.slice(2));
    if (options.changedContent) {
      const input = readFileSync(0, 'utf8');
      const changedFiles = input.split(options.null ? '\0' : /\r?\n/).filter(Boolean);
      const content = validateChangedReleaseContent({ changedFiles });
      console.log(JSON.stringify({ ok: true, mode: 'changed-content', ...content }, null, 2));
      return;
    }
    if (options.null) fail('--null requires --changed-content');
    if (!options.tag) fail('--tag is required');
    const content = validateReleaseContent({ tag: options.tag });
    const topology = options.contentOnly
      ? null
      : validateReleaseTopology({
          tag: options.tag,
          sha: options.sha,
          mainRef: options.mainRef || 'origin/main',
          devRef: options.devRef || 'origin/dev',
          requireTagRef: !options.dryRun,
        });
    console.log(
      JSON.stringify(
        {
          ok: true,
          tag: options.tag,
          prerelease: parseReleaseTag(options.tag).prerelease,
          content,
          topology,
        },
        null,
        2
      )
    );
  } catch (error) {
    console.error(
      `Release validation failed: ${error instanceof Error ? error.message : String(error)}`
    );
    process.exitCode = 1;
  }
};

const entryPoint = process.argv[1] ? path.resolve(process.argv[1]) : '';
if (entryPoint === fileURLToPath(import.meta.url)) runCli();
