import { describe, expect, it } from 'vitest';
import {
  classifyChangedFiles,
  findForbiddenUnicode,
  scanChangedTextFiles,
} from '../bin/ci/classify-pr-checks.mjs';

const noChecks = {
  frontend: false,
  manager_server: false,
  windows_sqlite: false,
  native_control: false,
  docker: false,
};

describe('PR check classifier', () => {
  it('fails closed when no changed files are available', () => {
    expect(classifyChangedFiles([])).toEqual({
      frontend: true,
      manager_server: true,
      windows_sqlite: true,
      native_control: true,
      docker: true,
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
    });
  });

  it('runs frontend checks for docs-site and README changes', () => {
    expect(classifyChangedFiles(['apps/docs/index.md', 'README.md'])).toEqual({
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
    });
  });

  it('runs all checks when the classifier or any workflow changes', () => {
    for (const filePath of [
      '.github/workflows/pr-check.yml',
      '.github/workflows/release.yml',
      'bin/ci/classify-pr-checks.mjs',
      'tests/prCheckClassifier.test.mjs',
    ]) {
      expect(classifyChangedFiles([filePath])).toEqual({
        frontend: true,
        manager_server: true,
        windows_sqlite: true,
        native_control: true,
        docker: true,
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
    });
  });

  it('detects forbidden invisible Unicode code points', () => {
    expect(findForbiddenUnicode('safe\u200Btext\u202E')).toEqual(['U+200B', 'U+202E']);
    expect(findForbiddenUnicode('plain text')).toEqual([]);
  });

  it('rejects changed paths that resolve outside the repository', () => {
    expect(scanChangedTextFiles(['../../outside.txt'])).toEqual([
      '../../outside.txt resolves outside the repository',
    ]);
  });
});
