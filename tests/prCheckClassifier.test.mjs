import { execFileSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
import {
  classifyChangedFiles,
  findForbiddenUnicode,
  parseChangedFilesInput,
  scanChangedTextFiles,
} from '../bin/ci/classify-pr-checks.mjs';

const noChecks = {
  frontend: false,
  manager_server: false,
  windows_sqlite: false,
  native_control: false,
  docker: false,
  demo: false,
  release_content: false,
};

describe('PR check classifier', () => {
  it('fails closed when no changed files are available', () => {
    expect(classifyChangedFiles([])).toEqual({
      frontend: true,
      manager_server: true,
      windows_sqlite: true,
      native_control: true,
      docker: true,
      demo: true,
      release_content: true,
    });
  });

  it('skips application checks for ordinary non-site docs changes', () => {
    expect(classifyChangedFiles(['docs/release.md'])).toEqual(noChecks);
  });

  it('runs frontend and Docker checks for web changes', () => {
    expect(classifyChangedFiles(['apps/web/src/features/login/LoginPage.tsx'])).toEqual({
      ...noChecks,
      frontend: true,
      docker: true,
      demo: true,
    });
  });

  it('runs frontend checks for README changes', () => {
    expect(classifyChangedFiles(['README.md'])).toEqual({
      ...noChecks,
      frontend: true,
    });
  });

  it('runs frontend, Docker, and demo builds for web changes', () => {
    expect(classifyChangedFiles(['apps/web/src/App.tsx'])).toEqual({
      ...noChecks,
      frontend: true,
      docker: true,
      demo: true,
    });
  });

  it('runs frontend checks for the legacy Pages redirect site', () => {
    expect(classifyChangedFiles(['bin/pages-redirect/redirect.mjs'])).toEqual({
      ...noChecks,
      frontend: true,
    });
  });

  it('runs release validation for release content and its validators', () => {
    expect(classifyChangedFiles(['docs/release-notes/v1.2.3-zh.md'])).toEqual({
      ...noChecks,
      release_content: true,
    });
    expect(classifyChangedFiles(['docs/release-posts/v1.2.3-telegram.html'])).toEqual({
      ...noChecks,
      release_content: true,
    });
    for (const filePath of [
      'bin/release/validate-release.mjs',
      'bin/release/validate-release-contributors.mjs',
    ]) {
      expect(classifyChangedFiles([filePath])).toEqual({
        ...noChecks,
        frontend: true,
        release_content: true,
      });
    }
  });

  it('runs Node tests for every release automation script', () => {
    for (const filePath of [
      'bin/release/send-telegram-release.sh',
      'bin/release/verify-published-release.mjs',
    ]) {
      expect(classifyChangedFiles([filePath])).toEqual({
        ...noChecks,
        frontend: true,
      });
    }
  });

  it('runs workflow integrity tests for Dependabot configuration changes', () => {
    expect(classifyChangedFiles(['.github/dependabot.yml'])).toEqual({
      ...noChecks,
      frontend: true,
    });
  });

  it('runs Linux, Windows SQLite, and Docker checks for manager-server changes', () => {
    expect(
      classifyChangedFiles(['apps/manager-server/internal/repository/sqlite/database.go'])
    ).toEqual({
      ...noChecks,
      manager_server: true,
      windows_sqlite: true,
      docker: true,
    });
  });

  it('runs native checks only for native control changes', () => {
    expect(classifyChangedFiles(['bin/native/cpa-manager-plusctl.ps1'])).toEqual({
      ...noChecks,
      native_control: true,
    });
  });

  it('runs build coverage for native packaging changes', () => {
    expect(classifyChangedFiles(['bin/release/package-native.sh'])).toEqual({
      ...noChecks,
      frontend: true,
      manager_server: true,
      windows_sqlite: true,
      native_control: true,
    });
  });

  it('runs Docker validation for Compose changes', () => {
    expect(classifyChangedFiles(['docker-compose.manager.yml'])).toEqual({
      ...noChecks,
      docker: true,
    });
  });

  it('runs Node and Docker checks for root dependency changes', () => {
    expect(classifyChangedFiles(['package-lock.json'])).toEqual({
      ...noChecks,
      frontend: true,
      native_control: true,
      docker: true,
      demo: true,
    });
  });

  it('runs all checks when the classifier or any workflow changes', () => {
    for (const filePath of [
      '.github/workflows/pr-check.yml',
      '.github/workflows/release.yml',
      'bin/ci/classify-pr-checks.mjs',
      'tests/prCheckClassifier.test.mjs',
      'tests/pagesRedirect.test.mjs',
    ]) {
      expect(classifyChangedFiles([filePath])).toEqual({
        frontend: true,
        manager_server: true,
        windows_sqlite: true,
        native_control: true,
        docker: true,
        demo: true,
        release_content: true,
      });
    }
  });

  it('normalizes CRLF input paths, Windows separators, blanks, and duplicates', () => {
    expect(
      classifyChangedFiles([
        'apps\\web\\src\\App.tsx\r',
        '',
        './apps/web/src/App.tsx',
        'apps/manager-server/cmd/cpa-manager-plus/main.go',
      ])
    ).toEqual({
      ...noChecks,
      frontend: true,
      manager_server: true,
      windows_sqlite: true,
      docker: true,
      demo: true,
    });
  });

  it('detects forbidden invisible Unicode code points', () => {
    expect(findForbiddenUnicode('safe\u200Btext\u202E')).toEqual(['U+200B', 'U+202E']);
    expect(findForbiddenUnicode('plain text')).toEqual([]);
  });

  it('preserves Git NUL-delimited Unicode paths for classification and scanning', () => {
    const repository = mkdtempSync(path.join(tmpdir(), 'cpamp-classifier-'));
    const relativePath = 'apps/web/安全\u200B检查.ts';

    try {
      execFileSync('git', ['init', '--quiet'], { cwd: repository });
      mkdirSync(path.dirname(path.join(repository, relativePath)), { recursive: true });
      writeFileSync(path.join(repository, relativePath), 'safe\u202Etext\n');
      execFileSync('git', ['add', relativePath], { cwd: repository });
      const changedFiles = parseChangedFilesInput(
        execFileSync('git', ['diff', '--cached', '--name-only', '--no-renames', '-z'], {
          cwd: repository,
          encoding: 'utf8',
        }),
        { nullDelimited: true }
      );

      expect(changedFiles).toEqual([relativePath]);
      expect(classifyChangedFiles(changedFiles)).toMatchObject({ frontend: true, docker: true });
      expect(scanChangedTextFiles(changedFiles, { root: repository })).toEqual([
        `${relativePath} contains U+202E`,
      ]);
    } finally {
      rmSync(repository, { recursive: true, force: true });
    }
  });

  it('rejects changed paths that resolve outside the repository', () => {
    expect(scanChangedTextFiles(['../../outside.txt'])).toEqual([
      '../../outside.txt resolves outside the repository',
    ]);
  });
});
