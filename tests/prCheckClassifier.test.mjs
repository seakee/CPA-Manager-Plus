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
  runtime_supervisor: false,
  ingress: false,
  windows_sqlite: false,
  windows_preflight: false,
  bundle_boundary: false,
  manager_runtime: false,
  runtime_lifecycle: false,
  native_control: false,
  docker: false,
  docker_all_images: false,
  demo_docs: false,
  release_content: false,
};

describe('PR check classifier', () => {
  it('runs repo checks for each AQ-01/02 evidence and runner path independently', () => {
    for (const filePath of [
      'bin/ci/validate-cpa-v8-evidence.mjs',
      'bin/ci/run-cpa-v8-qualification.mjs',
      'bin/ci/prepare-cpa-v8-artifact.py',
      'bin/ci/cpa-v8-external-fixtures.mjs',
      'tests/cpaV8ExternalSafety.test.mjs',
      'bin/ci/cpa-v8-usage-fixtures.mjs',
      'bin/ci/cpa-v8-stage-fixtures.mjs',
      'tests/cpaV8UsageStage.test.mjs',
      'bin/ci/cpa-v8-extension-fixtures.mjs',
      'tests/cpaV8ExtensionGuard.test.mjs',
      'tests/fixtures/cpa-v8-qualification/Dockerfile.runner',
      'tests/cpaV8QualificationRunner.test.mjs',
      'tests/fixtures/cpa-v8-qualification/manifest.json',
      'tests/fixtures/cpa-v8-qualification/README.md',
      'tests/cpaV8EvidenceContract.test.mjs',
    ]) {
      expect(classifyChangedFiles([filePath])).toEqual({ ...noChecks, frontend: true });
    }
    expect(classifyChangedFiles(['bin/ci/validate-cpa-v8-evidence-other.mjs'])).toEqual(noChecks);
  });

  // The toolchain baseline assertions live in tests/ciWorkflowIntegrity.test.mjs,
  // which the Frontend job runs, so a PR that only moves a baseline must still
  // run it (otherwise the Go/Node/image pins could regress unchecked).
  it('runs the Frontend job when only a toolchain baseline file changes', () => {
    for (const filePath of [
      '.nvmrc',
      'Dockerfile',
      'apps/manager-server/go.mod',
      'apps/ingress/go.mod',
      'apps/runtime-supervisor/go.mod',
    ]) {
      expect(classifyChangedFiles([filePath]).frontend, filePath).toBe(true);
    }
    expect(classifyChangedFiles(['docs/release.md']).frontend).toBe(false);
  });

  // The D acceptance and its native `node:test` lifecycle suite run in the
  // Frontend job. A PR that changes only one of these files must still run it,
  // and a neighbouring script that no job executes must not.
  it('runs the Node D lifecycle suite when only an Adopt D script changes', () => {
    for (const filePath of [
      'bin/ci/fresh-adopt-network.mjs',
      'bin/ci/fresh-adopt-lifecycle.test.mjs',
      'bin/ci/fresh-adopt-redaction.mjs',
    ]) {
      expect(classifyChangedFiles([filePath])).toEqual({ ...noChecks, frontend: true });
    }
    expect(classifyChangedFiles(['bin/ci/fresh-adopt-network-other.mjs'])).toEqual(noChecks);
  });

  // The Gate B/C readiness harness and its native `node:test` suite run in the
  // Frontend job. A PR that changes only the harness, its suite or the report
  // that quotes it must still schedule that job; a neighbouring evidence-free
  // docs path must not.
  it('runs the Gate B/C readiness harness when only its script, suite or report changes', () => {
    for (const filePath of [
      'bin/ci/readiness-gate-bc.mjs',
      'bin/ci/readiness-gate-bc.test.mjs',
      'docs/architecture/evidence/v2-beta-readiness/gate-bc-readiness-report.md',
      'docs/architecture/evidence/v2-beta-readiness/readiness-gate-bc-1a2b3c4d.json',
    ]) {
      expect(classifyChangedFiles([filePath])).toEqual({ ...noChecks, frontend: true });
    }
    expect(classifyChangedFiles(['bin/ci/readiness-gate-bc-other.mjs'])).toEqual(noChecks);
    expect(classifyChangedFiles(['docs/architecture/other/readiness.md'])).toEqual(noChecks);
  });

  // The Panel Lite acceptance and its native `node:test` suite run in the same
  // Frontend job. A PR that changes only one of them must still run that job.
  it('runs the Panel Lite harness suite when only a Panel Lite script changes', () => {
    for (const filePath of [
      'bin/ci/panel-lite-cpa-v8.mjs',
      'bin/ci/panel-lite-cpa-v8.test.mjs',
    ]) {
      expect(classifyChangedFiles([filePath])).toEqual({ ...noChecks, frontend: true });
    }
    expect(classifyChangedFiles(['bin/ci/panel-lite-cpa-v8-other.mjs'])).toEqual(noChecks);
  });

  // The design-token checker, its committed ratchet baseline and its runtime
  // property registry run in the Frontend job. A PR that changes only one of
  // them must still schedule that job.
  it('runs the Frontend job when only the design token checker, baseline or registry changes', () => {
    for (const filePath of [
      'bin/ci/check-design-tokens.mjs',
      'bin/ci/design-token-baseline.json',
      'bin/ci/design-token-runtime-properties.json',
      'tests/designTokenCheck.test.mjs',
    ]) {
      expect(classifyChangedFiles([filePath])).toEqual({ ...noChecks, frontend: true });
    }
    expect(classifyChangedFiles(['bin/ci/check-design-tokens-other.mjs'])).toEqual(noChecks);
  });

  it('fails closed when no changed files are available', () => {
    expect(classifyChangedFiles([])).toEqual({
      frontend: true,
      manager_server: true,
      runtime_supervisor: true,
      ingress: true,
      windows_sqlite: true,
      windows_preflight: true,
      bundle_boundary: true,
      manager_runtime: true,
      runtime_lifecycle: true,
      docker_all_images: true,
      native_control: true,
      docker: true,
      demo_docs: true,
      release_content: true,
    });
  });

  it('skips application checks for ordinary non-site docs changes', () => {
    expect(classifyChangedFiles(['docs/release.md'])).toEqual(noChecks);
  });

  it('retains full images for shared context and Runtime changes', () => {
    for (const filePath of [
      '.dockerignore',
      'docker/runtime/config.v8.default.seed.yaml',
      'apps/ingress/internal/ingress/proxy.go',
      'apps/runtime-supervisor/internal/lifecycle/recovery.go',
      'apps/manager-server/cmd/cpa-manager-plus/main.go',
      'bin/ci/runtime14-failure-e2e.sh',
    ]) {
      const checks = classifyChangedFiles([filePath]);
      expect(checks.docker, filePath).toBe(true);
      expect(checks.docker_all_images, filePath).toBe(true);
    }
  });

  it('omits unrelated Runtime and Ingress image builds for ordinary frontend and Manager changes', () => {
    for (const filePath of [
      'apps/web/src/features/login/LoginPage.tsx',
      'apps/manager-server/internal/worker/rate_limit_auto_disable.go',
      'package-lock.json',
    ]) {
      const checks = classifyChangedFiles([filePath]);
      expect(checks.docker, filePath).toBe(true);
      expect(checks.docker_all_images, filePath).toBe(false);
    }
  });

  it('retains full image builds for mixed ordinary and Runtime changes', () => {
    const checks = classifyChangedFiles([
      'apps/web/src/features/login/LoginPage.tsx',
      'apps/runtime-supervisor/internal/lifecycle/recovery.go',
    ]);
    expect(checks.docker).toBe(true);
    expect(checks.docker_all_images).toBe(true);
  });

  it('runs frontend and Docker checks for web changes', () => {
    expect(classifyChangedFiles(['apps/web/src/features/login/LoginPage.tsx'])).toEqual({
      ...noChecks,
      frontend: true,
      docker: true,
      demo_docs: true,
    });
  });

  it('runs frontend checks for docs-site and README changes', () => {
    expect(classifyChangedFiles(['apps/docs/index.md', 'README.md'])).toEqual({
      ...noChecks,
      frontend: true,
      demo_docs: true,
    });
  });

  it('runs Demo and Docs checks for web and docs-site changes', () => {
    expect(classifyChangedFiles(['apps/docs/index.md', 'apps/web/src/App.tsx'])).toEqual({
      ...noChecks,
      frontend: true,
      docker: true,
      demo_docs: true,
    });
  });

  it('runs release validation for release content and its validator', () => {
    expect(classifyChangedFiles(['docs/release-notes/v1.2.3-zh.md'])).toEqual({
      ...noChecks,
      release_content: true,
    });
    expect(classifyChangedFiles(['docs/release-posts/v1.2.3-telegram.html'])).toEqual({
      ...noChecks,
      release_content: true,
    });
    expect(classifyChangedFiles(['bin/release/validate-release.mjs'])).toEqual({
      ...noChecks,
      frontend: true,
      release_content: true,
    });
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

  it('runs SQLite, preflight, bundle, and Docker checks for SQLite changes', () => {
    expect(
      classifyChangedFiles(['apps/manager-server/internal/repository/sqlite/database.go'])
    ).toEqual({
      ...noChecks,
      manager_server: true,
      windows_sqlite: true,
      windows_preflight: true,
      bundle_boundary: true,
      docker: true,
    });
  });

  it('keeps ordinary Manager Server changes off unrelated heavy gates', () => {
    expect(
      classifyChangedFiles([
        'apps/manager-server/internal/repository/quotacooldown/repository.go',
        'apps/manager-server/internal/worker/rate_limit_auto_disable.go',
      ])
    ).toEqual({
      ...noChecks,
      manager_server: true,
      docker: true,
    });
  });

  it('runs Windows preflight and Runtime lifecycle checks for Manager startup wiring', () => {
    expect(
      classifyChangedFiles(['apps/manager-server/cmd/cpa-manager-plus/main.go'])
    ).toEqual({
      ...noChecks,
      manager_server: true,
      windows_preflight: true,
      manager_runtime: true,
      runtime_lifecycle: true,
      docker_all_images: true,
      docker: true,
    });
  });

  it('runs native checks only for native control changes', () => {
    expect(classifyChangedFiles(['bin/native/cpa-manager-plusctl.ps1'])).toEqual({
      ...noChecks,
      native_control: true,
    });
  });

  it('runs Supervisor, Runtime lifecycle, and Docker checks for Supervisor module changes', () => {
    for (const filePath of [
      'apps/runtime-supervisor/internal/protocol/handler.go',
      './apps\\runtime-supervisor\\cmd\\cpamp-runtime-supervisor\\main_test.go',
    ]) {
      expect(classifyChangedFiles([filePath])).toEqual({
        ...noChecks,
        runtime_supervisor: true,
        runtime_lifecycle: true,
        docker_all_images: true,
        docker: true,
      });
    }
    // The module manifest also declares the Go toolchain baseline, whose
    // integrity assertions run in the Frontend job.
    expect(classifyChangedFiles(['apps/runtime-supervisor/go.mod'])).toEqual({
      ...noChecks,
      frontend: true,
      runtime_supervisor: true,
      runtime_lifecycle: true,
      docker_all_images: true,
      docker: true,
    });
  });

  it('runs both necessary check sets for mixed Manager and Supervisor changes', () => {
    expect(
      classifyChangedFiles([
        'apps/manager-server/internal/service/runtime/embedded.go',
        'apps/runtime-supervisor/internal/protocol/handler.go',
      ])
    ).toEqual({
      ...noChecks,
      manager_server: true,
      runtime_supervisor: true,
      manager_runtime: true,
      runtime_lifecycle: true,
      docker_all_images: true,
      docker: true,
    });
  });

  it('runs Ingress, Runtime lifecycle, and Docker checks for Ingress changes', () => {
    expect(classifyChangedFiles(['apps/ingress/internal/ingress/proxy.go'])).toEqual({
      ...noChecks,
      ingress: true,
      runtime_lifecycle: true,
      docker_all_images: true,
      docker: true,
    });
    // Dockerfile carries the Ingress, Supervisor, and Manager builds plus the
    // image bases that tests/ciWorkflowIntegrity.test.mjs asserts.
    expect(classifyChangedFiles(['Dockerfile'])).toEqual({
      ...noChecks,
      frontend: true,
      runtime_supervisor: true,
      ingress: true,
      runtime_lifecycle: true,
      docker_all_images: true,
      docker: true,
    });
  });

  it('keeps mixed ordinary Manager and external Runtime changes off Manager Runtime gates', () => {
    expect(
      classifyChangedFiles([
        'apps/manager-server/internal/worker/rate_limit_auto_disable.go',
        'apps/ingress/internal/ingress/proxy.go',
      ])
    ).toEqual({
      ...noChecks,
      manager_server: true,
      ingress: true,
      runtime_lifecycle: true,
      docker_all_images: true,
      docker: true,
    });
  });

  it('runs Supervisor, Runtime lifecycle, and Docker checks for Runtime packaging changes', () => {
    for (const filePath of ['docker/runtime/entrypoint.sh', 'docker/runtime/config.seed.yaml']) {
      expect(classifyChangedFiles([filePath])).toEqual({
        ...noChecks,
        runtime_supervisor: true,
        runtime_lifecycle: true,
        docker_all_images: true,
        docker: true,
      });
    }
    // Dockerfile also carries the Go/Node image bases asserted by
    // tests/ciWorkflowIntegrity.test.mjs in the Frontend job.
    expect(classifyChangedFiles(['Dockerfile'])).toEqual({
      ...noChecks,
      frontend: true,
      runtime_supervisor: true,
      ingress: true,
      runtime_lifecycle: true,
      docker_all_images: true,
      docker: true,
    });
  });

  it('runs Supervisor checks when its architecture gate or fixtures change', () => {
    for (const filePath of [
      'bin/ci/runtime-boundary/main.go',
      'bin/ci/runtime-boundary/main_test.go',
    ]) {
      expect(classifyChangedFiles([filePath])).toEqual({
        ...noChecks,
        runtime_supervisor: true,
      });
    }
    expect(classifyChangedFiles(['apps/runtime-supervisor-extra/main.go'])).toEqual(noChecks);
  });

  it('runs build coverage for native packaging changes', () => {
    expect(classifyChangedFiles(['bin/release/package-native.sh'])).toEqual({
      ...noChecks,
      frontend: true,
      manager_server: true,
      windows_preflight: true,
      native_control: true,
    });
  });

  it('reruns native packaging tests when its license evidence inputs change', () => {
    for (const filePath of [
      'bin/release/collect-third-party-licenses.py',
      'bin/release/image-evidence.py',
      'bin/release/embedded-third-party-inventory.txt',
      'bin/release/third-party-license-sources/manifest.json',
    ]) {
      expect(classifyChangedFiles([filePath])).toEqual({
        ...noChecks,
        frontend: true,
        native_control: true,
      });
    }
    // The Full Web manifest generator is also the embedded-dist verifier.
    expect(classifyChangedFiles(['bin/release/hash-fullweb.mjs'])).toEqual({
      ...noChecks,
      frontend: true,
      native_control: true,
    });
  });

  it('runs Runtime lifecycle and Docker validation for Compose and Runtime E2E changes', () => {
    for (const filePath of [
      'docker-compose.yml',
      'docker-compose.manager.yml',
      'bin/ci/runtime12-docker-smoke.sh',
      'bin/ci/runtime14-failure-e2e.sh',
      'bin/ci/runtime17-activation-e2e.sh',
      'bin/ci/runtime18-secret-state-e2e.sh',
      'bin/ci/runtime19-phase1-exit-e2e.sh',
      'bin/ci/runtime-v8-default-smoke.py',
      'bin/ci/runtime-slim-acquire-smoke.py',
      'bin/ci/runtime-v7-compatibility.compose.yml',
      'bin/ci/validate-runtime12-compose.mjs',
    ]) {
      expect(classifyChangedFiles([filePath])).toEqual({
        ...noChecks,
        runtime_lifecycle: true,
        docker_all_images: true,
        docker: true,
      });
    }
  });

  it('runs the Runtime lifecycle smokes for the Fresh Manager runtime lane', () => {
    // The Fresh command files own the Manager half of the Runtime lifecycle:
    // the desired state, every submitted Start/Stop and the artifact an
    // automatic restart is allowed to launch. They live in the command package,
    // so they do not inherit the internal Runtime module path.
    for (const filePath of [
      'apps/manager-server/cmd/cpa-manager-plus/fresh.go',
      'apps/manager-server/cmd/cpa-manager-plus/fresh_runtime.go',
      'apps/manager-server/cmd/cpa-manager-plus/fresh_managed.go',
      'apps/manager-server/cmd/cpa-manager-plus/fresh_managed_migration.go',
      'apps/manager-server/internal/service/runtime/reconciler.go',
    ]) {
      const checks = classifyChangedFiles([filePath]);
      expect(checks.runtime_lifecycle, filePath).toBe(true);
      expect(checks.docker_all_images, filePath).toBe(true);
      expect(checks.docker, filePath).toBe(true);
    }
  });

  it('runs Docker failure validation for every combined Runtime lifecycle family', () => {
    for (const filePath of [
      'apps/ingress/internal/ingress/proxy.go',
      'apps/manager-server/cmd/cpa-manager-plus/main.go',
      'apps/manager-server/internal/app/app.go',
      'apps/manager-server/internal/config/config.go',
      'apps/manager-server/internal/model/runtime.go',
      'apps/manager-server/internal/http/router/router.go',
      'apps/manager-server/internal/http/controller/cpaupdate/handler.go',
      'apps/manager-server/internal/command/runtimeconfig/command.go',
      'apps/manager-server/internal/service/runtime/reconciler.go',
      'apps/manager-server/internal/repository/setting/repository.go',
      'apps/manager-server/internal/service/bootstrap/service.go',
      'apps/manager-server/internal/store/store.go',
      'apps/runtime-supervisor/internal/protocol/handler.go',
      'apps/runtime-supervisor/internal/lifecycle/recovery.go',
      'apps/runtime-supervisor/internal/readiness/readiness.go',
      'apps/runtime-supervisor/internal/journal/store.go',
      'apps/runtime-supervisor/internal/cpaprocess/process.go',
      'docker/runtime/entrypoint.sh',
      'Dockerfile',
      'docker-compose.yml',
      'bin/ci/runtime12-docker-smoke.sh',
      'bin/ci/runtime14-failure-e2e.sh',
      'bin/ci/runtime17-activation-e2e.sh',
      'bin/ci/runtime18-secret-state-e2e.sh',
      'bin/ci/runtime19-phase1-exit-e2e.sh',
    ]) {
      const checks = classifyChangedFiles([filePath]);
      expect(checks.docker, filePath).toBe(true);
      expect(checks.runtime_lifecycle, filePath).toBe(true);
      expect(
        checks.manager_runtime,
        filePath
      ).toBe(filePath.startsWith('apps/manager-server/'));
    }
  });

  it('runs Node and Docker checks for root dependency changes', () => {
    expect(classifyChangedFiles(['package-lock.json'])).toEqual({
      ...noChecks,
      frontend: true,
      native_control: true,
      docker: true,
      demo_docs: true,
    });
  });

  it('fails closed for the PR classifier and its own workflow', () => {
    for (const filePath of [
      '.github/workflows/pr-check.yml',
      'bin/ci/classify-pr-checks.mjs',
      'tests/prCheckClassifier.test.mjs',
    ]) {
      expect(classifyChangedFiles([filePath])).toEqual({
        frontend: true,
        manager_server: true,
        runtime_supervisor: true,
        ingress: true,
        windows_sqlite: true,
        windows_preflight: true,
        bundle_boundary: true,
        manager_runtime: true,
        runtime_lifecycle: true,
        docker_all_images: true,
        native_control: true,
        docker: true,
        demo_docs: true,
        release_content: true,
      });
    }
  });

  it('runs workflow integrity without expanding unrelated product gates', () => {
    for (const filePath of [
      '.github/workflows/release.yml',
      '.github/workflows/qualification-src11.yml',
    ]) {
      expect(classifyChangedFiles([filePath])).toEqual({
        ...noChecks,
        frontend: true,
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
      windows_preflight: true,
      manager_runtime: true,
      runtime_lifecycle: true,
      docker_all_images: true,
      docker: true,
      demo_docs: true,
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
