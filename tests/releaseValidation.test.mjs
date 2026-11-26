import { execFileSync, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  chmodSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { gunzipSync, gzipSync } from 'node:zlib';
import { afterEach, describe, expect, it } from 'vitest';
import {
  generateSlotKeypair,
  signSlotManifestBytes,
} from '../bin/release/cpamp-slot-sign.mjs';
import { packageSlotArchive } from '../bin/release/package-cpamp-slot.mjs';
import {
  extractSlotMetadataFromArchive,
  generateReleaseInfo,
} from '../bin/release/generate-release-info.mjs';
import { verifyLocalReleaseAssets } from '../bin/release/verify-published-release.mjs';
import {
  expectedV2AssetNames,
  v2DockerRoleTag,
  v2SlotArchiveName,
} from '../bin/release/update-contract.mjs';
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

describe('v2 beta release content continuity', () => {
  const v2Tag = 'v2.0.0-beta.1';
  it('validates the actual Beta draft only with both strict boolean gates', () => {
    expect(validateReleaseContent({ tag: v2Tag, dryRun: true, allowV2Beta: true })).toMatchObject({
      paths: { telegram: `docs/release-posts/${v2Tag}-telegram.html` },
    });
    // The first Beta must still direct 1.x users to migration, not an in-place update.
    const notes = readFileSync(new URL(`../docs/release-notes/${v2Tag}-zh.md`, import.meta.url), 'utf8');
    const metadata = JSON.parse(notes.match(/<!-- cpamp-update\n([\s\S]*?)-->/)[1]);
    expect(metadata.update).toMatchObject({
      migration_required: true,
      minimum_direct_upgrade_version: 'v1.14.10',
    });
    for (const options of [{}, { dryRun: true }, { allowV2Beta: true }]) {
      expect(() => validateReleaseContent({ tag: v2Tag, ...options })).toThrow(/fail-closed/);
    }
    for (const value of ['true', 'false', 1, 0, null]) {
      expect(() => validateReleaseContent({ tag: v2Tag, dryRun: value, allowV2Beta: true })).toThrow(/boolean/);
      expect(() => validateReleaseTopology({ tag: v2Tag, dryRun: true, allowV2Beta: value })).toThrow(/boolean/);
    }
  });

  it.each(['v2.0.0', 'v2.0.0-rc.1', 'v2.0.0-beta.01', 'v2.0.0-beta3'])(
    'rejects unsupported v2 version %s even during rehearsal', (tag) => {
      expect(() => validateReleaseContent({ tag, dryRun: true, allowV2Beta: true })).toThrow();
    }
  );

  it('binds the Beta rehearsal to the exact selected commit and committed release files', () => {
    const sha = 'a'.repeat(40);
    const calls = [];
    const git = (args) => {
      calls.push(args);
      if (args[0] === 'rev-parse' && args[2] === 'origin/v2^{commit}') return sha;
      if (args[0] === 'show') return readFileSync(new URL(`../${args[1].split(':')[1]}`, import.meta.url), 'utf8');
      throw new Error(`Unexpected git call: ${args.join(' ')}`);
    };
    expect(validateReleaseTopology({ tag: v2Tag, sha, v2Ref: 'origin/v2', dryRun: true, allowV2Beta: true, git }))
      .toMatchObject({ candidateSha: sha, sourceRef: 'origin/v2', sourceSha: sha });
    expect(calls.filter((args) => args[0] === 'show')).toHaveLength(3);
    expect(() => validateReleaseTopology({ tag: v2Tag, sha: 'b'.repeat(40), v2Ref: 'origin/v2', dryRun: true, allowV2Beta: true, git }))
      .toThrow('is not the current origin/v2');
    expect(() => validateReleaseTopology({ tag: v2Tag, sha, v2Ref: 'origin/v2', dryRun: true, allowV2Beta: true,
      git: (args) => args[0] === 'show' ? (() => { throw new Error('missing committed notes'); })() : git(args),
    })).toThrow('missing committed notes');
  });

  it('fails before IO without both Beta gates', () => {
    const readFile = () => { throw new Error('unexpected file read'); };
    const git = () => { throw new Error('unexpected git call'); };
    expect(() => validateReleaseContent({ tag: v2Tag, readFile })).toThrow(/fail-closed/);
    expect(() => validateReleaseTopology({ tag: v2Tag, dryRun: true, git })).toThrow(/fail-closed/);
  });

  it('validates newly added Beta content through the same changed-content PR gate', () => {
    const changedFiles = [`docs/release-notes/${v2Tag}-zh.md`, `docs/release-notes/${v2Tag}-en.md`, `docs/release-posts/${v2Tag}-telegram.html`];
    expect(() => validateChangedReleaseContent({ changedFiles })).toThrow(/fail-closed/);
    expect(validateChangedReleaseContent({ changedFiles, dryRun: true, allowV2Beta: true }).tags).toEqual([v2Tag]);
    const original = readFileSync(new URL(`../docs/release-notes/${v2Tag}-zh.md`, import.meta.url), 'utf8');
    const baseMetadata = JSON.parse(/<!-- cpamp-update\s*([\s\S]*?)-->/.exec(original)[1]);

    const validDocsMetadata = structuredClone(baseMetadata);
    validDocsMetadata.update = {
      breaking: false,
      migration_required: false,
      minimum_direct_upgrade_version: null,
      upgrade_guide_url: 'https://cpamp-docs.seakee.dev/next/operations/update.html',
    };
    expect(() =>
      validateReleaseContent({
        tag: v2Tag,
        dryRun: true,
        allowV2Beta: true,
        readFile: (filePath) =>
          filePath.endsWith('-zh.md')
            ? original.replace(
                /<!-- cpamp-update[\s\S]*?-->/,
                `<!-- cpamp-update\n${JSON.stringify(validDocsMetadata)}\n-->`
              )
            : readFileSync(filePath, 'utf8'),
      })
    ).not.toThrow();

    for (const [field, value] of [
      ['summary', { zh: '', en: 'Beta' }],
      [
        'update',
        {
          breaking: true,
          migration_required: 'no',
          minimum_direct_upgrade_version: 'v1.14.10',
          upgrade_guide_url: 'https://github.com/seakee/CPA-Manager-Plus/releases/tag/' + v2Tag,
        },
      ],
      [
        'update',
        {
          breaking: false,
          migration_required: false,
          upgrade_guide_url: 'https://cpamp-docs.seakee.dev/next/operations/update.html',
        },
      ],
      [
        'update',
        {
          breaking: false,
          migration_required: false,
          minimum_direct_upgrade_version: 'v2.0.0-beta.1',
          upgrade_guide_url: 'http://cpamp-docs.seakee.dev/next/operations/update.html',
        },
      ],
      ['compatibility', { minimum_cpa_version: 'v7.0.0' }],
    ]) {
      const metadata = structuredClone(baseMetadata);
      metadata[field] = value;
      expect(() =>
        validateReleaseContent({
          tag: v2Tag,
          dryRun: true,
          allowV2Beta: true,
          readFile: (filePath) =>
            filePath.endsWith('-zh.md')
              ? original.replace(
                  /<!-- cpamp-update[\s\S]*?-->/,
                  `<!-- cpamp-update\n${JSON.stringify(metadata)}\n-->`
                )
              : readFileSync(filePath, 'utf8'),
        })
      ).toThrow();
    }
  });

  it('accepts v2 Beta note pairs and keeps the prerelease flag', () => {
    expect(parseReleaseTag(v2Tag)).toEqual({ tag: v2Tag, prerelease: true });
    const chinese = `# CPA Manager Plus ${v2Tag}

[English ->](https://github.com/seakee/CPA-Manager-Plus/blob/${v2Tag}/docs/release-notes/${v2Tag}-en.md)
`;
    const english = `# CPA Manager Plus ${v2Tag}

[中文 ->](https://github.com/seakee/CPA-Manager-Plus/blob/${v2Tag}/docs/release-notes/${v2Tag}-zh.md)
`;
    expect(validateReleaseNotes({ tag: v2Tag, chinese, english })).toMatchObject({
      chineseCharacters: expect.any(Number),
      englishCharacters: expect.any(Number),
    });
    expect(() =>
      validateReleaseNotes({ tag: v2Tag, chinese, english: english.replaceAll(v2Tag, 'v2.0.0') })
    ).toThrow();
  });

  it('validates v2.0.0-beta.2 release notes, changed-content gate, and topology continuity', () => {
    const beta2Tag = 'v2.0.0-beta.2';
    expect(parseReleaseTag(beta2Tag)).toEqual({ tag: beta2Tag, prerelease: true });
    const sha = 'b'.repeat(40);
    const beta1Chinese = readFileSync(
      new URL('../docs/release-notes/v2.0.0-beta.1-zh.md', import.meta.url),
      'utf8'
    );
    const beta1English = readFileSync(
      new URL('../docs/release-notes/v2.0.0-beta.1-en.md', import.meta.url),
      'utf8'
    );
    const beta1Telegram = readFileSync(
      new URL('../docs/release-posts/v2.0.0-beta.1-telegram.html', import.meta.url),
      'utf8'
    );
    const beta2Metadata = {
      summary: {
        zh: 'v2.0.0-beta.2：离线 Ed25519 签名槽位自更新引擎。',
        en: 'v2.0.0-beta.2: offline Ed25519 signed-slot self-update engine.',
      },
      update: {
        breaking: false,
        migration_required: false,
        minimum_direct_upgrade_version: 'v2.0.0-beta.1',
        upgrade_guide_url: 'https://cpamp-docs.seakee.dev/next/operations/update.html',
      },
      compatibility: { minimum_cpa_version: 'v8.0.55' },
    };
    const beta2Chinese = beta1Chinese
      .replaceAll('v2.0.0-beta.1', beta2Tag)
      .replace(/<!-- cpamp-update[\s\S]*?-->/, `<!-- cpamp-update\n${JSON.stringify(beta2Metadata)}\n-->`);
    const beta2English = beta1English.replaceAll('v2.0.0-beta.1', beta2Tag);
    const beta2Telegram = beta1Telegram.replaceAll('v2.0.0-beta.1', beta2Tag);
    const files = {
      [`docs/release-notes/${beta2Tag}-zh.md`]: beta2Chinese,
      [`docs/release-notes/${beta2Tag}-en.md`]: beta2English,
      [`docs/release-posts/${beta2Tag}-telegram.html`]: beta2Telegram,
    };
    const resolveFixtureKey = (filePath) =>
      Object.keys(files).find((rel) => filePath === rel || filePath.endsWith(`/${rel}`));
    const readFile = (filePath) => {
      const key = resolveFixtureKey(filePath);
      if (!key) throw new Error(`Missing fixture file: ${filePath}`);
      return files[key];
    };
    const fileExists = (filePath) => Boolean(resolveFixtureKey(filePath));

    expect(
      validateReleaseContent({ tag: beta2Tag, dryRun: true, allowV2Beta: true, readFile, fileExists })
    ).toMatchObject({ paths: { telegram: `docs/release-posts/${beta2Tag}-telegram.html` } });
    expect(
      validateChangedReleaseContent({
        changedFiles: Object.keys(files),
        dryRun: true,
        allowV2Beta: true,
        readFile,
        fileExists,
      }).tags
    ).toEqual([beta2Tag]);
    expect(
      validateReleaseTopology({
        tag: beta2Tag,
        sha,
        v2Ref: 'origin/v2',
        dryRun: true,
        allowV2Beta: true,
        git: (args) => {
          if (args[0] === 'rev-parse' && args[2] === 'origin/v2^{commit}') return sha;
          if (args[0] === 'show') return readFile(args[1].split(':')[1]);
          throw new Error(`Unexpected git call: ${args.join(' ')}`);
        },
      })
    ).toMatchObject({ candidateSha: sha, sourceRef: 'origin/v2', sourceSha: sha });
    expect(() =>
      validateReleaseNotes({
        tag: beta2Tag,
        chinese: beta2Chinese,
        english: beta2English.replaceAll(beta2Tag, 'v2.0.0-beta.1'),
      })
    ).toThrow();
  });
});

describe('v2 slot archive verification in verify-published-release', () => {
  const tempDirs = [];
  const mkDir = (prefix) => {
    const dir = mkdtempSync(path.join(tmpdir(), prefix));
    tempDirs.push(dir);
    return dir;
  };
  afterEach(() => {
    const makeWritable = (target) => {
      const lst = lstatSync(target);
      if (lst.isSymbolicLink() || !lst.isDirectory()) return;
      chmodSync(target, 0o755);
      for (const child of readdirSync(target)) {
        makeWritable(path.join(target, child));
      }
    };
    while (tempDirs.length > 0) {
      const dir = tempDirs.pop();
      if (!existsSync(dir)) continue;
      makeWritable(dir);
      rmSync(dir, { recursive: true, force: true });
    }
  });

  const sha256Of = (value) => `sha256:${createHash('sha256').update(value).digest('hex')}`;
  const commit = 'b'.repeat(40);

  const stageSlotInput = () => {
    const root = mkDir('cpamp-relval-stage-');
    const writeMember = (rel, body, mode = 0o644) => {
      const full = path.join(root, rel);
      mkdirSync(path.dirname(full), { recursive: true });
      writeFileSync(full, body);
      chmodSync(full, mode);
    };
    writeMember('bin/cpamp-launcher', '#!/bin/sh\necho launcher\n', 0o755);
    writeMember('bin/cpamp-runtime-supervisor', '#!/bin/sh\necho supervisor\n', 0o755);
    writeMember('bin/cpa-manager-plus', '#!/bin/sh\necho manager\n', 0o755);
    writeMember('bin/cpamp-ingress', '#!/bin/sh\necho ingress\n', 0o755);
    writeMember(
      'share/cpamp/release/base-abi.json',
      JSON.stringify({ schema_version: 1, base_epoch: 1, platform: 'linux/amd64' }) + '\n',
      0o444
    );
    writeMember('share/cpamp/release/fullweb-manifest.json', '{"schemaVersion":1}\n', 0o444);
    writeMember('share/cpamp/release/public-api-transport-contract.json', '{"schemaVersion":1}\n', 0o444);
    writeMember('share/cpamp/runtime/cpa-artifact.json', '{"schemaVersion":1}\n', 0o444);
    writeMember('share/cpamp/runtime/config.seed.yaml', 'port: 8317\n', 0o644);
    writeMember('share/cpamp/runtime/seed-lineage.json', '{"schema_version":1}\n', 0o644);
    writeMember('share/licenses/cpamp/LICENSE', 'MIT\n', 0o444);
    return root;
  };

  const buildV2AssetsDir = (tag, slotArchivePath) => {
    const assetsDir = mkDir('cpamp-relval-assets-');
    const nativeDir = path.join(assetsDir, 'native');
    mkdirSync(nativeDir, { recursive: true });

    const compose = [
      'name: cpa-manager-plus',
      'services:',
      '  cpa-manager-plus:',
      `    image: seakee/cpa-manager-plus:${tag.slice(1)}-full@${sha256Of('full-manifest')}`,
      '',
    ].join('\n');
    const pairing = {
      schema_version: 1,
      version: tag,
      source_commit: commit,
      platform: 'linux/amd64',
      cpa: { version: '8.0.55', artifact_id: sha256Of('cpa') },
      fullweb: {
        manifest_sha256: sha256Of('fullweb-manifest'),
        index_html_sha256: sha256Of('management-html'),
        asset_count: 42,
      },
      api_contract: {
        sha256: sha256Of('api-contract'),
        contract_id: 'cpamp-r1-embedded-public-api-transport-v1',
      },
      compose: {
        filename: 'docker-compose.v2-embedded.yml',
        sha256: sha256Of(compose),
        size: Buffer.byteLength(compose, 'utf8'),
      },
      images: Object.fromEntries(
        ['slim', 'full'].map((role) => [
          role,
          {
            repository: 'seakee/cpa-manager-plus',
            tag: v2DockerRoleTag(tag, role),
            image_ref: `seakee/cpa-manager-plus:${v2DockerRoleTag(tag, role)}@${sha256Of(`${role}-manifest`)}`,
            manifest_digest: sha256Of(`${role}-manifest`),
            image_config_digest: sha256Of(`${role}-config`),
            binary_sha256: sha256Of(`${role}-binary`),
            release_provenance_sha256: sha256Of(`${role}-provenance`),
          },
        ])
      ),
    };
    const slotBytes = readFileSync(slotArchivePath);
    const slotName = v2SlotArchiveName(tag, 'amd64');
    const slotInfo = {
      assets: [
        {
          name: slotName,
          platform: 'linux/amd64',
          sha256: `sha256:${createHash('sha256').update(slotBytes).digest('hex')}`,
          size: slotBytes.length,
          min_base_epoch: 1,
          schema_generation: 1,
        },
      ],
    };
    const notes = `<!-- cpamp-update\n${JSON.stringify({
      summary: { zh: 'Beta 2', en: 'Beta 2' },
      update: {
        breaking: false,
        migration_required: false,
        minimum_direct_upgrade_version: 'v2.0.0-beta.1',
        upgrade_guide_url: 'https://cpamp-docs.seakee.dev/next/operations/update.html',
      },
      compatibility: { minimum_cpa_version: 'v8.0.55' },
    })}\n-->`;
    const info = generateReleaseInfo(tag, commit, notes, {
      dryRun: true,
      allowV2Beta: true,
      pairing,
      slot: slotInfo,
    });

    const checksums = [];
    const writeAsset = (relPath, body) => {
      const full = path.join(assetsDir, relPath);
      mkdirSync(path.dirname(full), { recursive: true });
      writeFileSync(full, body);
      checksums.push(`${createHash('sha256').update(body).digest('hex')}  ${relPath}\n`);
    };
    for (const name of expectedV2AssetNames(tag)) {
      if (name === 'checksums.txt') continue;
      if (name === 'release-info.json') {
        writeAsset(name, JSON.stringify(info, null, 2));
      } else if (name === 'embedded-release-pairing.json') {
        writeAsset(name, JSON.stringify(pairing, null, 2));
      } else if (name === 'docker-compose.v2-embedded.yml') {
        writeAsset(name, compose);
      } else if (name === slotName) {
        writeAsset(`native/${name}`, slotBytes);
      } else if (name.includes('_linux_amd64')) {
        writeAsset(`native/${name}`, `${name}\n`);
      } else {
        writeAsset(name, `${name}\n`);
      }
    }
    writeFileSync(path.join(assetsDir, 'checksums.txt'), checksums.join(''));
    return assetsDir;
  };

  it('rejects cpamp-root-2026-01 signed with an unrelated key and gates ephemeral dry-run keys in verify-published-release', () => {
    const tag = 'v2.0.0-beta.2';
    const stageRoot = stageSlotInput();
    const dryPair = generateSlotKeypair('cpamp-dry-run-2026');
    const validOut = mkDir('cpamp-relval-valid-');
    const validPkg = packageSlotArchive({
      root: stageRoot,
      version: tag,
      sourceCommit: commit,
      platform: 'linux/amd64',
      keyId: 'cpamp-dry-run-2026',
      privateKey: dryPair.privateKeyPem,
      publicKeyHex: dryPair.publicKeyHex,
      out: validOut,
    });
    const dryAssetsDir = buildV2AssetsDir(tag, validPkg.archivePath);

    const pubJsonDir = mkDir('cpamp-relval-pub-');
    const pubJsonPath = path.join(pubJsonDir, 'ephemeral-slot-signing-key.pub.json');
    writeFileSync(pubJsonPath, JSON.stringify(dryPair.publicRecord, null, 2) + '\n');

    // Dry-run rejects unknown cpamp-dry-run-* unless allowEphemeralSlotKey AND slotPublicKeyHex are provided
    expect(() =>
      verifyLocalReleaseAssets({ tag, assetsDir: dryAssetsDir, dryRun: true, allowV2Beta: true })
    ).toThrow(/Unknown untrusted slot key_id/);
    expect(() =>
      verifyLocalReleaseAssets({
        tag,
        assetsDir: dryAssetsDir,
        dryRun: true,
        allowV2Beta: true,
        allowEphemeralSlotKey: true,
      })
    ).toThrow(/Ephemeral slot key verification requires --slot-public-key/);
    expect(
      verifyLocalReleaseAssets({
        tag,
        assetsDir: dryAssetsDir,
        dryRun: true,
        allowV2Beta: true,
        allowEphemeralSlotKey: true,
        slotPublicKeyHex: dryPair.publicKeyHex,
      })
    ).toMatchObject({ complete: true, expectedAssets: 7 });

    const verifyScript = fileURLToPath(
      new URL('../bin/release/verify-published-release.mjs', import.meta.url)
    );
    const cliFail = spawnSync(
      process.execPath,
      [verifyScript, '--tag', tag, '--mode', 'assets', '--assets-dir', dryAssetsDir, '--dry-run', '--allow-v2-beta'],
      { encoding: 'utf8' }
    );
    expect(cliFail.status).not.toBe(0);
    expect(cliFail.stderr).toContain('Unknown untrusted slot key_id');

    const cliMissingKey = spawnSync(
      process.execPath,
      [
        verifyScript,
        '--tag',
        tag,
        '--mode',
        'assets',
        '--assets-dir',
        dryAssetsDir,
        '--dry-run',
        '--allow-v2-beta',
        '--allow-ephemeral-slot-key',
      ],
      { encoding: 'utf8' }
    );
    expect(cliMissingKey.status).not.toBe(0);
    expect(cliMissingKey.stderr).toContain('Ephemeral slot key verification requires --slot-public-key');

    const cliPass = spawnSync(
      process.execPath,
      [
        verifyScript,
        '--tag',
        tag,
        '--mode',
        'assets',
        '--assets-dir',
        dryAssetsDir,
        '--dry-run',
        '--allow-v2-beta',
        '--allow-ephemeral-slot-key',
        '--slot-public-key',
        pubJsonPath,
      ],
      { encoding: 'utf8' }
    );
    expect(cliPass.status).toBe(0);

    // Flipping 1 bit in the 64-byte Ed25519 signature of an ephemeral cpamp-dry-run-2026 archive fails verifyLocalReleaseAssets and CLI even with allowEphemeralSlotKey + slotPublicKeyHex
    const bitFlipExtractDir = mkDir('cpamp-relval-bitflip-extract-');
    execFileSync('tar', ['-xzf', validPkg.archivePath, '-C', bitFlipExtractDir]);
    const bitFlipSigPath = path.join(bitFlipExtractDir, 'slot-manifest.json.sig');
    chmodSync(bitFlipSigPath, 0o644);
    const bitFlipSigObj = JSON.parse(readFileSync(bitFlipSigPath, 'utf8'));
    const bitFlipSigBytes = Buffer.from(bitFlipSigObj.signature, 'base64');
    bitFlipSigBytes[0] ^= 0x01;
    bitFlipSigObj.signature = bitFlipSigBytes.toString('base64');
    writeFileSync(bitFlipSigPath, JSON.stringify(bitFlipSigObj, null, 2) + '\n');
    chmodSync(bitFlipSigPath, 0o444);
    const bitFlipArchivePath = path.join(
      mkDir('cpamp-relval-bitflip-out-'),
      v2SlotArchiveName(tag, 'amd64')
    );
    execFileSync('tar', ['-czf', bitFlipArchivePath, '-C', bitFlipExtractDir, '.']);
    execFileSync('chmod', ['-R', 'u+rwX', bitFlipExtractDir]);
    const bitFlipAssetsDir = buildV2AssetsDir(tag, bitFlipArchivePath);

    expect(() =>
      verifyLocalReleaseAssets({
        tag,
        assetsDir: bitFlipAssetsDir,
        dryRun: true,
        allowV2Beta: true,
        allowEphemeralSlotKey: true,
        slotPublicKeyHex: dryPair.publicKeyHex,
      })
    ).toThrow(/signature verification failed/);

    const cliBitFlip = spawnSync(
      process.execPath,
      [
        verifyScript,
        '--tag',
        tag,
        '--mode',
        'assets',
        '--assets-dir',
        bitFlipAssetsDir,
        '--dry-run',
        '--allow-v2-beta',
        '--allow-ephemeral-slot-key',
        '--slot-public-key',
        dryPair.publicKeyHex,
      ],
      { encoding: 'utf8' }
    );
    expect(cliBitFlip.status).not.toBe(0);
    expect(cliBitFlip.stderr).toContain('signature verification failed');

    // generate-release-info CLI --allow-ephemeral-slot-key --slot-public-key succeeds on valid archive and rejects bit-flipped signature
    const genScript = fileURLToPath(
      new URL('../bin/release/generate-release-info.mjs', import.meta.url)
    );
    const genWorkDir = mkDir('cpamp-relval-gencli-');
    mkdirSync(path.join(genWorkDir, 'docs', 'release-notes'), { recursive: true });
    writeFileSync(
      path.join(genWorkDir, 'docs', 'release-notes', `${tag}-zh.md`),
      `<!-- cpamp-update\n${JSON.stringify({
        summary: { zh: 'Beta 2', en: 'Beta 2' },
        update: {
          breaking: false,
          migration_required: false,
          minimum_direct_upgrade_version: 'v2.0.0-beta.1',
          upgrade_guide_url: 'https://cpamp-docs.seakee.dev/next/operations/update.html',
        },
        compatibility: { minimum_cpa_version: 'v8.0.55' },
      })}\n-->\n`
    );
    const pairingPath = path.join(dryAssetsDir, 'embedded-release-pairing.json');
    const genOutPath = path.join(genWorkDir, 'release-info.json');
    const genPass = spawnSync(
      process.execPath,
      [
        genScript,
        tag,
        genOutPath,
        commit,
        '--dry-run',
        '--allow-v2-beta',
        '--pairing',
        pairingPath,
        '--allow-ephemeral-slot-key',
        '--slot-public-key',
        pubJsonPath,
        '--slot-archive',
        validPkg.archivePath,
      ],
      { cwd: genWorkDir, encoding: 'utf8' }
    );
    expect(genPass.status, genPass.stderr).toBe(0);
    const genBitFlip = spawnSync(
      process.execPath,
      [
        genScript,
        tag,
        genOutPath,
        commit,
        '--dry-run',
        '--allow-v2-beta',
        '--pairing',
        pairingPath,
        '--allow-ephemeral-slot-key',
        '--slot-public-key',
        pubJsonPath,
        '--slot-archive',
        bitFlipArchivePath,
      ],
      { cwd: genWorkDir, encoding: 'utf8' }
    );
    expect(genBitFlip.status).not.toBe(0);
    expect(genBitFlip.stderr).toContain('signature verification failed');
    // Forged cpamp-root-2026-01 archive signed with an unrelated Ed25519 private key is ALWAYS rejected
    const extractDir = mkDir('cpamp-relval-forged-extract-');
    execFileSync('tar', ['-xzf', validPkg.archivePath, '-C', extractDir]);
    const manifestPath = path.join(extractDir, 'slot-manifest.json');
    const sigPath = path.join(extractDir, 'slot-manifest.json.sig');
    chmodSync(manifestPath, 0o644);
    chmodSync(sigPath, 0o644);
    const manifestObj = JSON.parse(readFileSync(manifestPath, 'utf8'));
    manifestObj.key_id = 'cpamp-root-2026-01';
    const forgedManifestText = JSON.stringify(manifestObj, null, 2) + '\n';
    writeFileSync(manifestPath, forgedManifestText);
    const unrelatedPair = generateSlotKeypair('cpamp-root-2026-01');
    const forgedSig = signSlotManifestBytes(
      Buffer.from(forgedManifestText, 'utf8'),
      unrelatedPair.privateKeyPem,
      'cpamp-root-2026-01'
    );
    writeFileSync(sigPath, JSON.stringify(forgedSig, null, 2) + '\n');
    chmodSync(manifestPath, 0o444);
    chmodSync(sigPath, 0o444);
    const forgedArchivePath = path.join(mkDir('cpamp-relval-forged-out-'), v2SlotArchiveName(tag, 'amd64'));
    execFileSync('tar', ['-czf', forgedArchivePath, '-C', extractDir, '.']);
    const forgedAssetsDir = buildV2AssetsDir(tag, forgedArchivePath);

    expect(() =>
      verifyLocalReleaseAssets({
        tag,
        assetsDir: forgedAssetsDir,
        dryRun: true,
        allowV2Beta: true,
        allowEphemeralSlotKey: true,
      })
    ).toThrow(/signature verification failed/);

    const cliForged = spawnSync(
      process.execPath,
      [
        verifyScript,
        '--tag',
        tag,
        '--mode',
        'assets',
        '--assets-dir',
        forgedAssetsDir,
        '--dry-run',
        '--allow-v2-beta',
        '--allow-ephemeral-slot-key',
      ],
      { encoding: 'utf8' }
    );
    expect(cliForged.status).not.toBe(0);
    expect(cliForged.stderr).toContain('signature verification failed');

    // Modifying 1 byte inside bin/cpa-manager-plus without re-signing slot-manifest.json
    // must fail BOTH extractSlotMetadataFromArchive (generate-release-info) AND verifyLocalReleaseAssets (verify-published-release)
    const rawTar = Buffer.from(gunzipSync(readFileSync(validPkg.archivePath)));
    let offset = 0;
    let flipped = false;
    while (offset + 512 <= rawTar.length) {
      const hdr = rawTar.subarray(offset, offset + 512);
      if (hdr.every((b) => b === 0)) break;
      const name = hdr.subarray(0, 100).toString('utf8').replace(/\0.*$/, '');
      const size = Number.parseInt(hdr.subarray(124, 136).toString('utf8').replace(/\0.*$/, '').trim() || '0', 8);
      const dataStart = offset + 512;
      if (name === 'bin/cpa-manager-plus') {
        rawTar[dataStart] ^= 0x01;
        flipped = true;
        break;
      }
      offset = dataStart + Math.ceil(size / 512) * 512;
    }
    expect(flipped).toBe(true);
    const modifiedArchivePath = path.join(mkDir('cpamp-relval-modbyte-out-'), v2SlotArchiveName(tag, 'amd64'));
    writeFileSync(modifiedArchivePath, gzipSync(rawTar));

    expect(() =>
      extractSlotMetadataFromArchive(modifiedArchivePath, tag, {
        dryRun: true,
        allowEphemeralSlotKey: true,
        slotPublicKeyHex: dryPair.publicKeyHex,
      })
    ).toThrow(/sha256 mismatch/);

    const genModified = spawnSync(
      process.execPath,
      [
        genScript,
        tag,
        genOutPath,
        commit,
        '--dry-run',
        '--allow-v2-beta',
        '--pairing',
        pairingPath,
        '--allow-ephemeral-slot-key',
        '--slot-public-key',
        pubJsonPath,
        '--slot-archive',
        modifiedArchivePath,
      ],
      { cwd: genWorkDir, encoding: 'utf8' }
    );
    expect(genModified.status).not.toBe(0);
    expect(genModified.stderr).toContain('sha256 mismatch');

    const modifiedAssetsDir = buildV2AssetsDir(tag, modifiedArchivePath);
    expect(() =>
      verifyLocalReleaseAssets({
        tag,
        assetsDir: modifiedAssetsDir,
        dryRun: true,
        allowV2Beta: true,
        allowEphemeralSlotKey: true,
        slotPublicKeyHex: dryPair.publicKeyHex,
      })
    ).toThrow(/sha256 mismatch/);

    const cliModified = spawnSync(
      process.execPath,
      [
        verifyScript,
        '--tag',
        tag,
        '--mode',
        'assets',
        '--assets-dir',
        modifiedAssetsDir,
        '--dry-run',
        '--allow-v2-beta',
        '--allow-ephemeral-slot-key',
        '--slot-public-key',
        pubJsonPath,
      ],
      { encoding: 'utf8' }
    );
    expect(cliModified.status).not.toBe(0);
    expect(cliModified.stderr).toContain('sha256 mismatch');
  });

  it('rejects a signed slot archive with 1 altered octal digit in a tar header checksum even when outer digests match', () => {
    const tag = 'v2.0.0-beta.2';
    const stageRoot = stageSlotInput();
    const dryPair = generateSlotKeypair('cpamp-dry-run-2026');
    const validOut = mkDir('cpamp-relval-chksum-valid-');
    const validPkg = packageSlotArchive({
      root: stageRoot,
      version: tag,
      sourceCommit: commit,
      platform: 'linux/amd64',
      keyId: 'cpamp-dry-run-2026',
      privateKey: dryPair.privateKeyPem,
      publicKeyHex: dryPair.publicKeyHex,
      out: validOut,
    });
    const pubJsonDir = mkDir('cpamp-relval-chksum-pub-');
    const pubJsonPath = path.join(pubJsonDir, 'ephemeral-slot-signing-key.pub.json');
    writeFileSync(pubJsonPath, JSON.stringify(dryPair.publicRecord, null, 2) + '\n');

    const rawTar = Buffer.from(gunzipSync(readFileSync(validPkg.archivePath)));
    let offset = 0;
    let altered = false;
    while (offset + 512 <= rawTar.length) {
      const hdr = rawTar.subarray(offset, offset + 512);
      if (hdr.every((b) => b === 0)) break;
      const name = hdr.subarray(0, 100).toString('utf8').replace(/\0.*$/, '');
      const size = Number.parseInt(
        hdr.subarray(124, 136).toString('utf8').replace(/\0.*$/, '').trim() || '0',
        8
      );
      if (name === 'bin/cpa-manager-plus') {
        let octalIdx = -1;
        for (let i = 154; i >= 148; i -= 1) {
          if (hdr[i] >= 0x30 && hdr[i] <= 0x37) {
            octalIdx = i;
            break;
          }
        }
        expect(octalIdx).toBeGreaterThanOrEqual(148);
        expect(octalIdx).toBeLessThanOrEqual(154);
        rawTar[offset + octalIdx] ^= 0x01;
        altered = true;
        break;
      }
      offset = offset + 512 + Math.ceil(size / 512) * 512;
    }
    expect(altered).toBe(true);

    const tamperedGz = gzipSync(rawTar);
    const tamperedArchivePath = path.join(
      mkDir('cpamp-relval-chksum-out-'),
      v2SlotArchiveName(tag, 'amd64')
    );
    writeFileSync(tamperedArchivePath, tamperedGz);

    expect(() =>
      extractSlotMetadataFromArchive(tamperedArchivePath, tag, {
        dryRun: true,
        allowEphemeralSlotKey: true,
        slotPublicKeyHex: dryPair.publicKeyHex,
      })
    ).toThrow(/invalid tar header checksum/);

    const assetsDir = buildV2AssetsDir(tag, tamperedArchivePath);
    const info = JSON.parse(readFileSync(path.join(assetsDir, 'release-info.json'), 'utf8'));
    expect(info.distribution.slot.assets[0]).toMatchObject({
      name: v2SlotArchiveName(tag, 'amd64'),
      sha256: sha256Of(tamperedGz),
      size: tamperedGz.length,
    });

    expect(() =>
      verifyLocalReleaseAssets({
        tag,
        assetsDir,
        dryRun: true,
        allowV2Beta: true,
        allowEphemeralSlotKey: true,
        slotPublicKeyHex: dryPair.publicKeyHex,
      })
    ).toThrow(/invalid tar header checksum/);

    const verifyScript = fileURLToPath(
      new URL('../bin/release/verify-published-release.mjs', import.meta.url)
    );
    const cliVerify = spawnSync(
      process.execPath,
      [
        verifyScript,
        '--tag',
        tag,
        '--mode',
        'assets',
        '--assets-dir',
        assetsDir,
        '--dry-run',
        '--allow-v2-beta',
        '--allow-ephemeral-slot-key',
        '--slot-public-key',
        pubJsonPath,
      ],
      { encoding: 'utf8' }
    );
    expect(cliVerify.status).not.toBe(0);
    expect(cliVerify.stderr).toContain('invalid tar header checksum');
  });
});
