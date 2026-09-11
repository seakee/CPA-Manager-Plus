import { execFileSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
import {
  parseReleaseTag,
  validateChangedReleaseContent,
  validateReleaseContent,
  validateReleaseNotes,
  validateReleaseTopology,
  validateTelegramHtml,
} from '../bin/release/validate-release.mjs';

const releaseTag = 'v1.2.3';
const releasePaths = {
  chinese: `docs/release-notes/${releaseTag}-zh.md`,
  english: `docs/release-notes/${releaseTag}-en.md`,
  telegram: `docs/release-posts/${releaseTag}-telegram.html`,
};

const validMetadata = {
  summary: {
    zh: '更新说明',
    en: 'Release update',
  },
  update: {
    breaking: false,
    migration_required: false,
    minimum_direct_upgrade_version: null,
    upgrade_guide_url: `https://github.com/seakee/CPA-Manager-Plus/releases/tag/${releaseTag}`,
  },
  compatibility: {
    minimum_cpa_version: null,
  },
};

const makeChineseNotes = (metadata = validMetadata) => `# CPA Manager Plus ${releaseTag}

[English ->](https://github.com/seakee/CPA-Manager-Plus/blob/${releaseTag}/docs/release-notes/${releaseTag}-en.md)

<!-- cpamp-update
${typeof metadata === 'string' ? metadata : JSON.stringify(metadata, null, 2)}
-->
`;

const chineseNotes = makeChineseNotes();
const notesWithoutMetadata = `# CPA Manager Plus ${releaseTag}

[English ->](https://github.com/seakee/CPA-Manager-Plus/blob/${releaseTag}/docs/release-notes/${releaseTag}-en.md)
`;
const englishNotes = `# CPA Manager Plus ${releaseTag}

[中文 ->](https://github.com/seakee/CPA-Manager-Plus/blob/${releaseTag}/docs/release-notes/${releaseTag}-zh.md)
`;
const telegramPost = '<b>v1.2.3</b>\n\n• Release update';

describe('release tag validation', () => {
  it('accepts stable and prerelease SemVer tags', () => {
    expect(parseReleaseTag('v1.2.3')).toEqual({ tag: 'v1.2.3', prerelease: false });
    expect(parseReleaseTag('v1.2.3-rc.1')).toEqual({ tag: 'v1.2.3-rc.1', prerelease: true });
  });

  it.each([
    '1.2.3',
    'v1.2',
    'v01.2.3',
    'v1.2.3-',
    'v1.2.3-01',
    'v1.2.3-rc.01',
    'v1.2.3+build.1',
    'vfoo',
  ])('rejects invalid tag %s', (tag) => {
    expect(() => parseReleaseTag(tag)).toThrow();
  });
});

describe('release content validation', () => {
  it('requires reciprocal tag-pinned language links', () => {
    expect(
      validateReleaseNotes({ tag: releaseTag, chinese: chineseNotes, english: englishNotes })
    ).toMatchObject({
      chineseCharacters: expect.any(Number),
      englishCharacters: expect.any(Number),
    });
    expect(() =>
      validateReleaseNotes({
        tag: releaseTag,
        chinese: chineseNotes,
        english: englishNotes.replace(`/blob/${releaseTag}/`, '/blob/v0.0.0/'),
      })
    ).toThrow('English release notes must link');
  });

  it('rejects unsupported Telegram markup and Markdown-only sections', () => {
    expect(validateTelegramHtml(telegramPost)).toEqual({ characters: expect.any(Number) });
    expect(validateTelegramHtml('<b>R&amp;D &#62; &#X3E; baseline</b>')).toEqual({
      characters: expect.any(Number),
    });
    expect(() => validateTelegramHtml('<script>alert(1)</script>')).toThrow('unsupported HTML');
    expect(() => validateTelegramHtml('<b>发布截图</b>')).toThrow('Markdown-only');
    expect(() => validateTelegramHtml('<a href="http://example.com">link</a>')).toThrow('HTTPS');
    expect(() => validateTelegramHtml('<b>R&D</b>')).toThrow('invalid or unescaped');
    expect(() => validateTelegramHtml('<b>A > B</b>')).toThrow('unescaped >');
    expect(() => validateTelegramHtml('<b>&bogus;</b>')).toThrow('invalid or unescaped');
  });

  it('requires all three release files before publishing', () => {
    const contents = new Map([
      [releasePaths.chinese, chineseNotes],
      [releasePaths.english, englishNotes],
      [releasePaths.telegram, telegramPost],
    ]);
    const readFile = (filePath) => contents.get(filePath.split('/').slice(-3).join('/'));
    const fileExists = (filePath) => contents.has(filePath.split('/').slice(-3).join('/'));

    expect(validateReleaseContent({ tag: releaseTag, readFile, fileExists })).toMatchObject({
      paths: releasePaths,
    });
    contents.delete(releasePaths.telegram);
    expect(() => validateReleaseContent({ tag: releaseTag, readFile, fileExists })).toThrow(
      'Missing required release files'
    );
  });

  it('requires valid cpamp-update metadata in Chinese release notes', () => {
    const contents = new Map([
      [releasePaths.chinese, chineseNotes],
      [releasePaths.english, englishNotes],
      [releasePaths.telegram, telegramPost],
    ]);
    const readFile = (filePath) => contents.get(filePath.split('/').slice(-3).join('/'));
    const fileExists = (filePath) => contents.has(filePath.split('/').slice(-3).join('/'));

    // Valid release content passes.
    expect(validateReleaseContent({ tag: releaseTag, readFile, fileExists })).toMatchObject({
      paths: releasePaths,
    });

    // Missing metadata block fails.
    contents.set(releasePaths.chinese, notesWithoutMetadata);
    expect(() => validateReleaseContent({ tag: releaseTag, readFile, fileExists })).toThrow(
      'Release notes require exactly one cpamp-update JSON comment'
    );

    // Malformed JSON fails.
    contents.set(releasePaths.chinese, makeChineseNotes('{ invalid json'));
    expect(() => validateReleaseContent({ tag: releaseTag, readFile, fileExists })).toThrow();

    // Missing required field (breaking) fails.
    const missingBreaking = JSON.parse(JSON.stringify(validMetadata));
    delete missingBreaking.update.breaking;
    contents.set(releasePaths.chinese, makeChineseNotes(missingBreaking));
    expect(() => validateReleaseContent({ tag: releaseTag, readFile, fileExists })).toThrow(
      'Explicit update flags required'
    );
  });

  it('validates every release tag represented by changed release paths and fails on bad metadata', () => {
    const contents = new Map([
      [releasePaths.chinese, chineseNotes],
      [releasePaths.english, englishNotes],
      [releasePaths.telegram, telegramPost],
    ]);
    const readFile = (filePath) => contents.get(filePath.split('/').slice(-3).join('/'));
    const fileExists = (filePath) => contents.has(filePath.split('/').slice(-3).join('/'));

    expect(
      validateChangedReleaseContent({
        changedFiles: Object.values(releasePaths),
        readFile,
        fileExists,
      })
    ).toMatchObject({
      tags: [releaseTag],
      releases: [expect.objectContaining({ paths: releasePaths })],
    });

    // changed-content gate fails when metadata is missing
    contents.set(releasePaths.chinese, notesWithoutMetadata);
    expect(() =>
      validateChangedReleaseContent({
        changedFiles: Object.values(releasePaths),
        readFile,
        fileExists,
      })
    ).toThrow('Release notes require exactly one cpamp-update JSON comment');

    contents.set(releasePaths.chinese, chineseNotes);
    expect(() =>
      validateChangedReleaseContent({
        changedFiles: ['docs/release-notes/README.md'],
        readFile,
        fileExists,
      })
    ).toThrow('Unexpected release content path');
  });
});

const makeTopologyGit = ({ extraDevChange = false, promotionTreeDrift = false, tagSha } = {}) => {
  const mainSha = 'a'.repeat(40);
  const previousMainSha = 'b'.repeat(40);
  const devSha = 'c'.repeat(40);
  const releaseParentSha = 'd'.repeat(40);
  const tagTarget = tagSha || mainSha;
  const mainTree = '1'.repeat(40);
  const devTree = promotionTreeDrift ? '2'.repeat(40) : mainTree;
  return {
    mainSha,
    devSha,
    git: (args) => {
      if (args[0] === 'rev-parse' && args[2] === 'origin/main^{commit}') return mainSha;
      if (args[0] === 'rev-parse' && args[2] === 'origin/dev^{commit}') return devSha;
      if (args[0] === 'rev-parse' && args[2] === 'refs/tags/v1.2.3^{commit}') return tagTarget;
      if (args[0] === 'rev-parse' && args[2] === `${mainSha}^{tree}`) return mainTree;
      if (args[0] === 'rev-parse' && args[2] === `${devSha}^{tree}`) return devTree;
      if (args[0] === 'rev-list' && args.at(-1) === mainSha)
        return `${mainSha} ${previousMainSha} ${devSha}`;
      if (args[0] === 'rev-list' && args.at(-1) === devSha)
        return `${devSha} ${releaseParentSha} ${'e'.repeat(40)}`;
      if (args[0] === 'diff') {
        const changes = Object.values(releasePaths).map((filePath) => `A\t${filePath}`);
        if (extraDevChange) changes.push('A\tREADME.md');
        return changes.join('\n');
      }
      throw new Error(`Unexpected git call: ${args.join(' ')}`);
    },
  };
};

describe('release topology validation', () => {
  it('requires the tag to point to the current main promotion merge', () => {
    const topology = makeTopologyGit();
    expect(
      validateReleaseTopology({ tag: releaseTag, sha: topology.mainSha, git: topology.git })
    ).toMatchObject({
      mainSha: topology.mainSha,
      devSha: topology.devSha,
    });
  });

  it('rejects a tag that points to an older or unrelated commit', () => {
    const topology = makeTopologyGit({ tagSha: 'f'.repeat(40) });
    expect(() =>
      validateReleaseTopology({ tag: releaseTag, sha: topology.mainSha, git: topology.git })
    ).toThrow('does not point to the candidate');
  });

  it('rejects dev advancement or extra changes after the release PR', () => {
    const topology = makeTopologyGit({ extraDevChange: true });
    expect(() =>
      validateReleaseTopology({ tag: releaseTag, sha: topology.mainSha, git: topology.git })
    ).toThrow('not the exact release PR merge');
  });

  it('rejects a promotion merge whose final tree differs from dev', () => {
    const topology = makeTopologyGit({ promotionTreeDrift: true });
    expect(() =>
      validateReleaseTopology({ tag: releaseTag, sha: topology.mainSha, git: topology.git })
    ).toThrow('tree must exactly match');
  });

  it('validates a complete topology using real Git commands', () => {
    const repository = mkdtempSync(path.join(tmpdir(), 'cpamp-release-topology-'));
    const git = (args) =>
      execFileSync('git', args, {
        cwd: repository,
        encoding: 'utf8',
        stdio: ['ignore', 'pipe', 'pipe'],
      }).trim();

    try {
      git(['init', '--quiet', '--initial-branch=main']);
      git(['config', 'user.name', 'CI Test']);
      git(['config', 'user.email', 'ci-test@example.invalid']);
      git(['config', 'commit.gpgsign', 'false']);
      writeFileSync(path.join(repository, 'base.txt'), 'base\n');
      git(['add', 'base.txt']);
      git(['commit', '--quiet', '-m', 'base']);

      git(['switch', '--quiet', '-c', 'dev']);
      git(['switch', '--quiet', '-c', `release/${releaseTag}`]);
      mkdirSync(path.join(repository, 'docs', 'release-notes'), { recursive: true });
      mkdirSync(path.join(repository, 'docs', 'release-posts'), { recursive: true });
      for (const filePath of Object.values(releasePaths)) {
        writeFileSync(path.join(repository, filePath), `${filePath}\n`);
      }
      git(['add', 'docs']);
      git(['commit', '--quiet', '-m', 'release content']);

      git(['switch', '--quiet', 'dev']);
      git(['merge', '--quiet', '--no-ff', `release/${releaseTag}`, '-m', 'merge release']);
      const devSha = git(['rev-parse', 'HEAD']);
      git(['switch', '--quiet', 'main']);
      git(['merge', '--quiet', '--no-ff', 'dev', '-m', 'promote dev']);
      const mainSha = git(['rev-parse', 'HEAD']);
      git(['tag', releaseTag, mainSha]);

      expect(
        validateReleaseTopology({
          tag: releaseTag,
          sha: mainSha,
          mainRef: 'refs/heads/main',
          devRef: 'refs/heads/dev',
          git,
        })
      ).toMatchObject({ mainSha, devSha });
    } finally {
      rmSync(repository, { recursive: true, force: true });
    }
  });
});
