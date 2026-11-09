import { spawnSync } from 'node:child_process';
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { parse as parseYaml } from 'yaml';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

const compareVersions = (left, right) => {
  const parts = (value) => value.split('-')[0].split('.').map((part) => Number(part));
  const [major, minor, patch] = parts(left);
  const [otherMajor, otherMinor, otherPatch] = parts(right);
  return major - otherMajor || minor - otherMinor || patch - otherPatch;
};

const expectAtLeast = (version, floor, label) => {
  expect(version, `${label} must declare a pinned version`).toMatch(/^\d+\.\d+(?:\.\d+)?$/);
  expect(
    compareVersions(version, floor),
    `${label}: ${version} must be at least ${floor}`
  ).toBeGreaterThanOrEqual(0);
};

const workflowDir = path.join(repoRoot, '.github', 'workflows');
const readWorkflow = (name) => readFileSync(path.join(workflowDir, name), 'utf8');
const dependabotConfig = readFileSync(path.join(repoRoot, '.github', 'dependabot.yml'), 'utf8');
const telegramScript = readFileSync(
  path.join(repoRoot, 'bin', 'release', 'send-telegram-release.sh'),
  'utf8'
);

const externalActions = (workflow) =>
  [...workflow.matchAll(/^\s*uses:\s*([^\s#]+)@([^\s#]+)/gm)]
    .map(([, action, ref]) => ({ action, ref }))
    .filter(({ action }) => !action.startsWith('./'));

const jobBlock = (workflow, jobName) => {
  const lines = workflow.split('\n');
  const start = lines.findIndex((line) => line === `  ${jobName}:`);
  if (start === -1) throw new Error(`Missing workflow job: ${jobName}`);
  const relativeEnd = lines.slice(start + 1).findIndex((line) => /^  \S/.test(line));
  const end = relativeEnd === -1 ? lines.length : start + 1 + relativeEnd;
  return lines.slice(start + 1, end).join('\n');
};

const stepBlock = (job, stepName) => {
  const lines = job.split('\n');
  const start = lines.findIndex((line) => line === `      - name: ${stepName}`);
  if (start === -1) throw new Error(`Missing workflow step: ${stepName}`);
  const relativeEnd = lines.slice(start + 1).findIndex((line) => /^      - /.test(line));
  const end = relativeEnd === -1 ? lines.length : start + 1 + relativeEnd;
  return lines.slice(start, end).join('\n');
};

describe('GitHub Actions workflow integrity', () => {
  it('pins every external action to a full commit SHA', () => {
    const workflowFiles = readdirSync(workflowDir).filter((file) => /\.ya?ml$/.test(file));
    const actions = workflowFiles.flatMap((file) => externalActions(readWorkflow(file)));

    expect(actions.length).toBeGreaterThan(0);
    for (const { action, ref } of actions) {
      expect(ref, `${action} must be pinned to a 40-character commit SHA`).toMatch(
        /^[0-9a-f]{40}$/
      );
    }
  });

  it('retargets non-dev pull requests without executing contributor code', () => {
    const workflow = readWorkflow('main-promotion.yml');
    const targetJob = jobBlock(workflow, 'retarget');

    expect(workflow).toContain('pull_request_target:');
    expect(workflow).toContain('branches-ignore:');
    expect(workflow).toContain("github.event.pull_request.base.ref != 'dev'");
    expect(workflow).toContain(
      'github.event.pull_request.head.repo.full_name != github.repository'
    );
    expect(targetJob).toContain('PR_TARGET_APP_ID');
    expect(targetJob).toContain('PR_TARGET_PRIVATE_KEY');
    expect(targetJob).toContain('permission-pull-requests: write');
    expect(targetJob).toContain('github.rest.pulls.update');
    expect(targetJob).toContain('base: "dev"');
    expect(targetJob).not.toContain('actions/checkout@');
    expect(targetJob).not.toContain('github.event.pull_request.head.sha');
  });

  it('keeps the main promotion check on the pull request merge commit', () => {
    const workflow = readWorkflow('main-promotion.yml');
    const promotionJob = jobBlock(workflow, 'verify-source');

    expect(workflow).toContain('pull_request:');
    expect(workflow).toContain('- main');
    expect(promotionJob).toContain('name: Verify dev promotion source');
    expect(promotionJob).toContain("if: github.event_name == 'pull_request'");
    expect(promotionJob).toContain('HEAD_REPOSITORY');
    expect(promotionJob).toContain('HEAD_REF');
    expect(promotionJob).toContain('main accepts promotions only from');
  });

  it('reruns pull request checks after an automated base change', () => {
    const workflow = readWorkflow('pr-check.yml');
    const trigger = workflow.slice(0, workflow.indexOf('\npermissions:'));

    expect(trigger).toContain('pull_request:');
    expect(trigger).toContain('types:');
    expect(trigger).toContain('- edited');
  });

  it('keeps Demo and Docs inside the stable required-check aggregate', () => {
    const workflow = readWorkflow('pr-check.yml');
    const demoJob = jobBlock(workflow, 'demo-docs');
    const requiredJob = jobBlock(workflow, 'required');

    expect(demoJob).toContain('name: Demo and Docs');
    expect(requiredJob).toContain('- demo-docs');
    expect(requiredJob).toContain("DEMO_DOCS_RESULT: ${{ needs['demo-docs'].result }}");
    expect(requiredJob).toContain('"Demo and Docs:${DEMO_DOCS_RESULT}"');
  });

  it('always calls the security workflow and waits for it in Required checks', () => {
    const workflow = parseYaml(readWorkflow('pr-check.yml'));
    const security = parseYaml(readWorkflow('security.yml'));

    expect(workflow.jobs.security.uses).toBe('./.github/workflows/security.yml');
    expect(workflow.jobs.security.if).toBeUndefined();
    expect(workflow.jobs.security['continue-on-error']).toBeUndefined();
    expect(workflow.jobs.required.needs).toContain('security');
    expect(workflow.jobs.required.if).toBe('always()');
    expect(security.on).toHaveProperty('workflow_call');
    expect(security.on).toHaveProperty('schedule');
    expect(security.on).toHaveProperty('workflow_dispatch');
  });

  it.each(['success', 'failure', 'cancelled', 'skipped', '', undefined])(
    'only passes Required checks when Dependency Security succeeds (%s)',
    (securityResult) => {
      const workflow = parseYaml(readWorkflow('pr-check.yml'));
      const step = workflow.jobs.required.steps.find(
        (candidate) => candidate.name === 'Verify applicable checks'
      );
      expect(step.env.SECURITY_RESULT).toBe('${{ needs.security.result }}');
      const env = {
        ...process.env,
        ...Object.fromEntries(Object.keys(step.env).map((key) => [key, 'skipped'])),
      };
      if (securityResult === undefined) delete env.SECURITY_RESULT;
      else env.SECURITY_RESULT = securityResult;

      const result = spawnSync('bash', ['-c', step.run], { env, encoding: 'utf8' });
      expect(result.error).toBeUndefined();
      if (securityResult === 'success') expect(result.status, result.stderr).toBe(0);
      else expect(result.status).not.toBe(0);
    }
  );

  it('scans every Go module for the six shipped target platforms', () => {
    const security = parseYaml(readWorkflow('security.yml'));
    const goJob = security.jobs.go;
    const npmJob = security.jobs.npm;

    // v2 ships linux/amd64 + linux/arm64 images, darwin/amd64 + darwin/arm64
    // native archives and windows/amd64 native and portability builds, so the
    // standard-library audit keeps the complete six-target matrix.
    expect(goJob.strategy.matrix.goos).toEqual(['linux', 'darwin', 'windows']);
    expect(goJob.strategy.matrix.goarch).toEqual(['amd64', 'arm64']);
    expect(goJob.steps.find((step) => step.name === 'Setup Go').with).toEqual({
      'go-version-file': 'apps/manager-server/go.mod',
      'cache-dependency-path':
        'apps/manager-server/go.sum\napps/ingress/go.mod\napps/runtime-supervisor/go.sum\n',
    });
    const scan = goJob.steps.find((step) => step.name === 'Scan application dependencies');
    expect(scan.env.GOOS).toBe('${{ matrix.goos }}');
    expect(scan.env.GOARCH).toBe('${{ matrix.goarch }}');
    expect(scan.env.CGO_ENABLED).toBe('0');

    const npmAudit = npmJob.steps.find(
      (step) => step.name === 'Audit all workspace dependencies'
    );
    expect(npmAudit.run).toBe('npm audit --package-lock-only --ignore-scripts');
    expectAtLeast(
      npmJob.steps.find((step) => step.name === 'Setup Node.js').with['node-version'],
      '22.23.4',
      'security.yml npm node-version'
    );
  });

  it('scans every Go module and fails the Go audit when any module reports a finding', () => {
    const security = parseYaml(readWorkflow('security.yml'));
    const scan = security.jobs.go.steps.find(
      (step) => step.name === 'Scan application dependencies'
    );
    const stubDir = mkdtempSync(path.join(tmpdir(), 'govulncheck-stub-'));
    const logFile = path.join(stubDir, 'invocations.log');
    writeFileSync(
      path.join(stubDir, 'govulncheck'),
      [
        '#!/bin/sh',
        'set -eu',
        'echo "$PWD" >> "$GOVULNCHECK_LOG"',
        'if [ -n "$GOVULNCHECK_FAIL_SUFFIX" ]; then',
        '  case "$PWD" in',
        '    *"$GOVULNCHECK_FAIL_SUFFIX") echo "Vulnerability found" >&2; exit 3;;',
        '  esac',
        'fi',
        'exit 0',
        '',
      ].join('\n'),
      { mode: 0o755 }
    );

    const runScan = (failSuffix) => {
      rmSync(logFile, { force: true });
      const result = spawnSync('bash', ['-c', scan.run], {
        cwd: repoRoot,
        encoding: 'utf8',
        env: {
          ...process.env,
          PATH: `${stubDir}:${process.env.PATH}`,
          GOVULNCHECK_LOG: logFile,
          GOVULNCHECK_FAIL_SUFFIX: failSuffix,
          GOOS: 'linux',
          GOARCH: 'amd64',
          CGO_ENABLED: '0',
        },
      });
      return {
        ...result,
        scanned: existsSync(logFile)
          ? readFileSync(logFile, 'utf8').trim().split('\n').filter(Boolean)
          : [],
      };
    };

    try {
      const passing = runScan('');
      expect(passing.error).toBeUndefined();
      expect(passing.status, passing.stderr).toBe(0);
      expect(passing.scanned.map((dir) => path.relative(repoRoot, dir))).toEqual([
        'apps/manager-server',
        'apps/ingress',
        'apps/runtime-supervisor',
      ]);

      const failing = runScan('apps/ingress');
      expect(failing.status).not.toBe(0);
      expect(failing.scanned.map((dir) => path.relative(repoRoot, dir))).toEqual([
        'apps/manager-server',
        'apps/ingress',
      ]);
    } finally {
      rmSync(stubDir, { recursive: true, force: true });
    }
  });

  it('pins the Go and Node security baselines in manifests, images and CI', () => {
    const workflows = readdirSync(workflowDir).filter((file) => /\.ya?ml$/.test(file));
    const readRepoFile = (relative) => readFileSync(path.join(repoRoot, relative), 'utf8');
    const goDirective = (module) =>
      readRepoFile(`apps/${module}/go.mod`).match(/^go (\d+\.\d+\.\d+)$/m)?.[1];

    const goVersions = {};
    for (const module of ['manager-server', 'ingress', 'runtime-supervisor']) {
      goVersions[module] = goDirective(module);
      expectAtLeast(goVersions[module], '1.26.10', `apps/${module}/go.mod go directive`);
    }
    // The toolchain must not fork: setup-go reads the manifest, the images bake
    // the Dockerfile version, so all three manifests and all three build stages
    // have to agree exactly (the floors above only stop downgrades).
    expect(new Set(Object.values(goVersions)).size, JSON.stringify(goVersions)).toBe(1);
    for (const module of ['manager-server', 'runtime-supervisor']) {
      const requirement = readRepoFile(`apps/${module}/go.mod`).match(
        /golang\.org\/x\/sys v(\d+\.\d+\.\d+)/
      );
      expect(requirement, `${module} must require golang.org/x/sys`).toBeTruthy();
      expectAtLeast(requirement[1], '0.44.0', `${module} golang.org/x/sys`);
    }

    expectAtLeast(readRepoFile('.nvmrc').trim(), '24.22.1', '.nvmrc');
    const dockerfileText = readRepoFile('Dockerfile');
    expectAtLeast(
      dockerfileText.match(/node:(\d+\.\d+\.\d+)-alpine/)?.[1],
      '22.23.4',
      'Dockerfile node base'
    );
    expect(dockerfileText, 'Dockerfile').not.toContain('golang:1.24');
    expect(dockerfileText, 'Dockerfile').not.toMatch(/node:\d+-alpine/);

    const imageGoMatches = [...dockerfileText.matchAll(/golang:(\d+\.\d+\.\d+)-alpine/g)].map(
      (match) => match[1]
    );
    expect(imageGoMatches.length).toBeGreaterThanOrEqual(3);
    for (const version of imageGoMatches) {
      expectAtLeast(version, '1.26.10', 'Dockerfile golang base');
    }
    expect(new Set(imageGoMatches).size, JSON.stringify(imageGoMatches)).toBe(1);
    expect(
      imageGoMatches[0],
      'build images must match the module Go directive'
    ).toBe(Object.values(goVersions)[0]);

    for (const file of workflows) {
      const text = readWorkflow(file);
      for (const match of text.matchAll(/(?:node|go)-version:\s*'([^']+)'/g)) {
        // An unpinned major such as '24' or '22' would silently float the
        // toolchain, so every pin has to carry a patch version.
        expect(match[1], `${file} ${match[0]}`).toMatch(/^\d+\.\d+\.\d+$/);
      }
      for (const match of text.matchAll(/^\s*GO_VERSION:\s*'([^']+)'/gm)) {
        expectAtLeast(match[1], '1.26.10', `${file} GO_VERSION`);
      }
      for (const match of text.matchAll(/^\s*NODE_VERSION:\s*'([^']+)'/gm)) {
        expectAtLeast(match[1], '22.23.4', `${file} NODE_VERSION`);
      }
    }
  });

  it('installs the Go toolchain before any job runs the Go-dependent repo tests', () => {
    const workflow = parseYaml(readWorkflow('pr-check.yml'));
    const repoTestStep = /^npm run test(?::repo)?$/m;
    let checkedJobs = 0;

    for (const [name, job] of Object.entries(workflow.jobs)) {
      if (!Array.isArray(job.steps)) continue;
      const testIndex = job.steps.findIndex((step) => repoTestStep.test(step.run ?? ''));
      if (testIndex < 0) continue;
      checkedJobs += 1;
      const setupIndex = job.steps.findIndex((step) => /^actions\/setup-go@/.test(step.uses ?? ''));
      expect(setupIndex, `${name} must install Go`).toBeGreaterThanOrEqual(0);
      expect(setupIndex, `${name} must install Go before ${job.steps[testIndex].name}`).toBeLessThan(
        testIndex
      );
      // The repo tests read the module manifests through `go mod edit -json`, so
      // the setup has to be driven by a manifest rather than a floating major.
      expect(job.steps[setupIndex].with['go-version-file']).toMatch(/^apps\/.*\/go\.mod$/);
      for (const [index, step] of job.steps.entries()) {
        if (!/^go\s/.test((step.run ?? '').trim())) continue;
        expect(index, `${name} ${step.name} runs go`).toBeGreaterThan(setupIndex);
      }
    }

    expect(checkedJobs).toBeGreaterThan(0);
  });

  it('runs the design token check in Frontend after npm ci and Lint and before Test', () => {
    const workflow = parseYaml(readWorkflow('pr-check.yml'));
    const steps = workflow.jobs.frontend.steps;
    const installIndex = steps.findIndex(
      (step) => step.name === 'Install dependencies' && step.run === 'npm ci'
    );
    const lintIndex = steps.findIndex(
      (step) => step.name === 'Lint' && step.run === 'npm run lint'
    );
    const checkTokensIndex = steps.findIndex((step) => step.name === 'Check design tokens');
    const testIndex = steps.findIndex(
      (step) => step.name === 'Test' && step.run === 'npm run test'
    );

    expect(installIndex).toBeGreaterThanOrEqual(0);
    expect(lintIndex).toBeGreaterThan(installIndex);
    expect(checkTokensIndex).toBeGreaterThan(lintIndex);
    expect(steps[checkTokensIndex].run).toBe('npm run check:design-tokens');
    expect(testIndex).toBeGreaterThan(checkTokensIndex);
  });

  it('locks the dependency versions that close the known advisories', () => {
    const lock = JSON.parse(readFileSync(path.join(repoRoot, 'package-lock.json'), 'utf8'));
    const locked = (name) => lock.packages[`node_modules/${name}`]?.version;
    const atLeast = (name, minimum) => {
      const version = locked(name);
      expect(version, name).toBeTruthy();
      const [major, minor, patch] = version.split('.').map(Number);
      const [minMajor, minMinor, minPatch] = minimum.split('.').map(Number);
      expect(
        major - minMajor || minor - minMinor || patch - minPatch,
        `${name}@${version} must be at least ${minimum}`
      ).toBeGreaterThanOrEqual(0);
    };

    // zod < 4.6.6 exhausts memory while validating oversized arrays
    // (GHSA-mrhx-c7xc-m3w5). It is a dev-only peer of eslint-plugin-react-hooks,
    // but the workspace audit gates every PR, so the lock must stay above it.
    atLeast('zod', '4.6.6');
    // The runtime and build stack the workspace audit covers.
    atLeast('axios', '1.20.0');
    atLeast('react-router-dom', '7.18.2');
    atLeast('vite', '8.1.6');
    atLeast('vite-plugin-singlefile', '2.3.4');
    atLeast('vitest', '4.1.12');

    // Advisories published after v1.14.10: follow-redirects inbound-header leak
    // (GHSA-mrwp-f3q8-3g9p, patched 1.16.2) and the VitePress build Vite copy
    // (GHSA-8g5g-mxqh-grgj, patched 6.4.5).
    atLeast('follow-redirects', '1.16.2');
    expectAtLeast(
      lock.packages['node_modules/vitepress/node_modules/vite']?.version,
      '6.4.5',
      'VitePress build vite'
    );

    // GHSA-g3hv-75wg-m249 (minimatch < 11.0.1) is closed only inside the
    // typescript-eslint path; eslint's own minimatch 3 line, which filelist,
    // jake and ejs still need, must stay untouched.
    expectAtLeast(
      lock.packages['node_modules/@typescript-eslint/typescript-estree/node_modules/minimatch']
        ?.version,
      '11.0.1',
      'typescript-estree minimatch'
    );
    expect(locked('minimatch')).toMatch(/^3\./);

    // The scoped VitePress overrides keep the docs build on React 19 compatible
    // DocSearch packages instead of the advisories in its default resolution.
    expect(locked('@docsearch/css')).toBe('3.9.0');
    expect(locked('@docsearch/js')).toBe('3.9.0');
  });

  it('runs Supervisor verification through classification and the required-check aggregate', () => {
    const workflow = readWorkflow('pr-check.yml');
    const scopeJob = jobBlock(workflow, 'changes');
    const supervisorJob = jobBlock(workflow, 'runtime-supervisor');
    const supervisorWindowsJob = jobBlock(workflow, 'runtime-supervisor-windows');
    const requiredJob = jobBlock(workflow, 'required');

    expect(scopeJob).toContain(
      'runtime_supervisor: ${{ steps.classify.outputs.runtime_supervisor }}'
    );
    expect(supervisorJob).toContain("if: needs.changes.outputs.runtime_supervisor == 'true'");
    expect(supervisorJob).toContain('working-directory: apps/runtime-supervisor');
    expect(supervisorJob).toContain('cache-dependency-path: apps/runtime-supervisor/go.mod');
    expect(supervisorJob).toContain('run: go test ./...');
    expect(supervisorJob).toContain('name: Test deterministic Runtime prepare and staging');
    for (const testName of [
      'TestSourceResolvesOnlyExactOfficialReleaseAsset',
      'TestSourceBoundsMetadataAndArchiveAndVerifiesDigest',
      'TestStoreStagesOnlyExactExecutableWithExactIdentity',
      'TestStoreReusesOnlyVerifiedImmutableFinalStageAcrossReopen',
    ]) {
      expect(supervisorJob).toContain(testName);
    }
    expect(supervisorJob).toContain('run: go vet ./...');
    for (const target of ['linux/amd64', 'linux/arm64', 'windows/amd64']) {
      expect(supervisorJob).toContain(target);
    }
    expect(supervisorJob).toContain('CGO_ENABLED=0');
    expect(supervisorJob).toContain('go build -trimpath');
    expect(supervisorJob).toContain(
      'run: go test ./bin/ci/runtime-boundary/main.go ./bin/ci/runtime-boundary/main_test.go'
    );
    expect(supervisorJob).toContain('run: go run ./bin/ci/runtime-boundary/main.go');
    expect(supervisorWindowsJob).toContain('name: Runtime Supervisor (Windows)');
    expect(supervisorWindowsJob).toContain(
      "if: needs.changes.outputs.runtime_supervisor == 'true'"
    );
    expect(supervisorWindowsJob).toContain('runs-on: windows-latest');
    expect(supervisorWindowsJob).toContain('working-directory: apps/runtime-supervisor');
    expect(supervisorWindowsJob).toContain(
      "go test ./internal/selection -run '^TestSelectionStoreAcceptsPlatformNativeDirectoryPermissions$' -count=1"
    );
    expect(requiredJob).toContain('- runtime-supervisor');
    expect(requiredJob).toContain('- runtime-supervisor-windows');
    expect(requiredJob).toContain(
      "RUNTIME_SUPERVISOR_RESULT: ${{ needs['runtime-supervisor'].result }}"
    );
    expect(requiredJob).toContain(
      "RUNTIME_SUPERVISOR_WINDOWS_RESULT: ${{ needs['runtime-supervisor-windows'].result }}"
    );
    expect(requiredJob).toContain('"Runtime Supervisor:${RUNTIME_SUPERVISOR_RESULT}"');
    expect(requiredJob).toContain(
      '"Runtime Supervisor (Windows):${RUNTIME_SUPERVISOR_WINDOWS_RESULT}"'
    );
  });

  it('runs default v8 checks before explicit historical v7 Runtime regressions', () => {
    const dockerJob = jobBlock(readWorkflow('pr-check.yml'), 'docker-build');
    const lifecycleCondition = "if: needs.changes.outputs.runtime_lifecycle == 'true'";
    const lifecycleSteps = dockerJob
      .split(/^      - name: /m)
      .slice(1)
      .filter((step) => step.includes(lifecycleCondition));

    expect(lifecycleSteps.map((step) => step.split('\n')[0])).toEqual([
      'Build native smoke images',
      'Run normal default CPA v8 Compose smoke',
      'Run slim runtime CPA acquisition smoke',
      'Reject unsupported CPA v8 architecture without fallback',
      'Select explicit v7 compatibility image for historical regressions',
      'Run single-public-port Runtime smoke',
      'Run Runtime failure-behavior E2E',
      'Run Runtime activation and rollback E2E',
      'Run Runtime secret and state segmentation E2E',
      'Run Runtime Phase1 exit E2E',
    ]);
    expect(lifecycleSteps[0]).toContain('run: docker compose build');
    expect(lifecycleSteps[1]).toContain('python3 bin/ci/runtime-v8-default-smoke.py');
    expect(lifecycleSteps[2]).toContain('python3 bin/ci/runtime-slim-acquire-smoke.py');
    expect(lifecycleSteps[3]).toContain('--platform linux/riscv64 --target cpa-v8-download');
    // The refusal must still exercise the architecture check, so the build
    // carries valid identity instead of failing the early identity guard.
    expect(lifecycleSteps[3]).toContain('--build-arg VERSION=ci');
    expect(lifecycleSteps[3]).toContain('--build-arg SOURCE_COMMIT=${{ github.sha }}');
    expect(lifecycleSteps[3]).toContain(
      'unsupported official CPA v8 target: linux/riscv64 (no v7 fallback)'
    );
    expect(lifecycleSteps[4]).toContain(
      'run: docker compose -f docker-compose.yml -f bin/ci/runtime-v7-compatibility.compose.yml build cpa-manager-plus'
    );
    const focusedSmoke = dockerJob.indexOf('run: bin/ci/runtime12-docker-smoke.sh');
    const failureE2E = dockerJob.indexOf('run: bin/ci/runtime14-failure-e2e.sh');
    const activationE2E = dockerJob.indexOf('run: bin/ci/runtime17-activation-e2e.sh');
    const segmentationE2E = dockerJob.indexOf('run: bin/ci/runtime18-secret-state-e2e.sh');
    const exitE2E = dockerJob.indexOf('run: bin/ci/runtime19-phase1-exit-e2e.sh');

    expect(focusedSmoke).toBeGreaterThan(-1);
    expect(failureE2E).toBeGreaterThan(focusedSmoke);
    expect(activationE2E).toBeGreaterThan(failureE2E);
    expect(segmentationE2E).toBeGreaterThan(activationE2E);
    expect(exitE2E).toBeGreaterThan(segmentationE2E);
    expect(dockerJob).toContain("CPAMP_RUNTIME14_PORT: '18317'");
    expect(dockerJob).toContain("CPAMP_RUNTIME14_SKIP_BUILD: 'true'");
    expect(dockerJob).toContain("CPAMP_RUNTIME17_PORT: '18317'");
    expect(dockerJob).toContain("CPAMP_RUNTIME17_SKIP_BUILD: 'true'");
    expect(dockerJob).toContain("CPAMP_RUNTIME18_PORT: '18317'");
    expect(dockerJob).toContain("CPAMP_RUNTIME18_SKIP_BUILD: 'true'");
    expect(dockerJob).toContain("CPAMP_RUNTIME19_PORT: '18317'");
    expect(dockerJob).toContain("CPAMP_RUNTIME19_SKIP_BUILD: 'true'");
    expect(dockerJob).not.toContain('continue-on-error');
    expect(
      existsSync(path.join(repoRoot, 'bin', 'ci', 'runtime19-phase1-exit-e2e.sh'))
    ).toBe(true);
  });

  it('passes fail-closed image identity to every Docker build caller', () => {
    const imageDockerfiles = ['Dockerfile'];
    const mergeCommit = '${{ github.sha }}';
    const readYaml = (relative) => parseYaml(readFileSync(path.join(repoRoot, relative), 'utf8'));
    const stepsOf = (parsed) => Object.values(parsed.jobs).flatMap((job) => job.steps ?? []);
    const imageBuilds = (parsed) =>
      stepsOf(parsed).filter((step) => step.with && imageDockerfiles.includes(step.with.file));

    // PR images compile the checked-out merge commit, so the stamp must be
    // github.sha: the PR head SHA names different bytes when the base moved.
    const pullRequest = readYaml('.github/workflows/pr-check.yml');
    const dockerBuilds = imageBuilds(pullRequest);
    expect(dockerBuilds.map((step) => [step.with.file, step.with.target])).toEqual([
      ['Dockerfile', 'slim'],
      ['Dockerfile', 'slim'],
      ['Dockerfile', 'full'],
    ]);
    for (const step of dockerBuilds) {
      expect(step.with['build-args'], step.name).toContain('VERSION=ci');
      expect(step.with['build-args'], step.name).toContain(`SOURCE_COMMIT=${mergeCommit}`);
    }

    const composeSteps = stepsOf(pullRequest).filter(
      (step) =>
        typeof step.run === 'string' &&
        step.run.includes('docker compose') &&
        /\bbuild\b/.test(step.run)
    );
    expect(composeSteps).toHaveLength(2);
    for (const step of composeSteps) {
      expect(step.env?.VERSION, step.name).toBe('ci');
      expect(step.env?.SOURCE_COMMIT, step.name).toBe(mergeCommit);
    }

    // Release images are built from the tagged commit and must carry the exact
    // expected expressions; a truncated or emptied value must not pass.
    const expectedIdentity = {
      '.github/workflows/release.yml': {
        VERSION: '${{ env.RELEASE_TAG }}',
        SOURCE_COMMIT: '${{ github.sha }}',
      },
      '.github/workflows/release-publish-recovery.yml': {
        VERSION: '${{ env.RELEASE_TAG }}',
        SOURCE_COMMIT: '${{ env.RELEASE_SHA }}',
      },
    };
    const identityLines = (args) =>
      String(args ?? '')
        .trim()
        .split('\n')
        .map((line) => line.trim());
    const hasExactIdentity = (args, expected) => {
      const lines = identityLines(args);
      return (
        lines.length === 2 &&
        lines[0] === `VERSION=${expected.VERSION}` &&
        lines[1] === `SOURCE_COMMIT=${expected.SOURCE_COMMIT}`
      );
    };
    for (const [file, expected] of Object.entries(expectedIdentity)) {
      const releaseBuilds = imageBuilds(readYaml(file));
      expect(releaseBuilds.length, file).toBeGreaterThan(0);
      for (const step of releaseBuilds) {
        expect(identityLines(step.with['build-args']), `${file}: ${step.name}`).toEqual([
          `VERSION=${expected.VERSION}`,
          `SOURCE_COMMIT=${expected.SOURCE_COMMIT}`,
        ]);
        expect(hasExactIdentity(step.with['build-args'], expected), `${file}: ${step.name}`).toBe(
          true
        );
      }
      const exact = `VERSION=${expected.VERSION}\nSOURCE_COMMIT=${expected.SOURCE_COMMIT}`;
      expect(hasExactIdentity(exact, expected), file).toBe(true);
      // Mutating or emptying either expression must fail the exact check.
      expect(hasExactIdentity(`VERSION=\nSOURCE_COMMIT=${expected.SOURCE_COMMIT}`, expected), file).toBe(
        false
      );
      expect(hasExactIdentity(`VERSION=${expected.VERSION}\nSOURCE_COMMIT=`, expected), file).toBe(
        false
      );
      expect(hasExactIdentity(`SOURCE_COMMIT=${expected.SOURCE_COMMIT}`, expected), file).toBe(false);
      expect(hasExactIdentity(`VERSION=${expected.VERSION}`, expected), file).toBe(false);
    }

    // Every buildable Compose service forwards both args. The empty
    // SOURCE_COMMIT default keeps local builds fail-closed.
    for (const file of ['docker-compose.yml', 'docker-compose.manager.yml']) {
      const services = readYaml(file).services;
      const buildable = Object.entries(services).filter(([, service]) => service.build);
      expect(buildable.length, file).toBeGreaterThan(0);
      for (const [name, service] of buildable) {
        expect(imageDockerfiles, `${file}: ${name}`).toContain(service.build.dockerfile);
        expect(service.build.args?.VERSION, `${file}: ${name}`).toBe('${VERSION:-dev}');
        expect(service.build.args?.SOURCE_COMMIT, `${file}: ${name}`).toBe('${SOURCE_COMMIT:-}');
      }
    }

    // The local CI scripts derive the same identity from the checkout itself.
    const head = spawnSync('git', ['rev-parse', 'HEAD'], { cwd: repoRoot, encoding: 'utf8' })
      .stdout.trim();
    expect(head).toMatch(/^[0-9a-f]{40}$/);
    const quotedRoot = JSON.stringify(repoRoot);
    for (const script of [
      'runtime12-docker-smoke.sh',
      'runtime14-failure-e2e.sh',
      'runtime17-activation-e2e.sh',
      'runtime18-secret-state-e2e.sh',
      'runtime19-phase1-exit-e2e.sh',
    ]) {
      const lines = readFileSync(path.join(repoRoot, 'bin', 'ci', script), 'utf8').split('\n');
      const lastExport = lines.map((line) => line.startsWith('export SOURCE_COMMIT=')).lastIndexOf(true);
      expect(lastExport, script).toBeGreaterThan(0);
      const header = lines
        .slice(0, lastExport + 1)
        // `bash -c` leaves BASH_SOURCE empty, so pin the worktree root instead.
        .map((line) => (line.startsWith('repo_root=') ? `repo_root=${quotedRoot}` : line))
        .join('\n');
      const result = spawnSync(
        'bash',
        ['-c', `${header}\nprintf '%s\\n%s' "$VERSION" "$SOURCE_COMMIT"`],
        { cwd: repoRoot, encoding: 'utf8', env: { ...process.env, VERSION: '', SOURCE_COMMIT: '' } }
      );
      expect(result.status, `${script}: ${result.stderr}`).toBe(0);
      const [version, commit] = result.stdout.split('\n');
      expect(version, script).toMatch(/^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$/);
      expect(version.toLowerCase(), script).not.toBe('unknown');
      expect(commit, script).toBe(head);
    }
  });

  it('keeps Manager Runtime race, vet, and Windows portability gates', () => {
    const workflow = readWorkflow('pr-check.yml');
    const managerJob = jobBlock(workflow, 'manager-server');
    const windowsJob = jobBlock(workflow, 'manager-server-windows-sqlite');

    expect(managerJob).toContain(
      'go test -race ./internal/service/cpaupdate ./internal/service/runtime ./internal/repository/setting ./internal/service/bootstrap'
    );
    expect(managerJob).toContain("if: needs.changes.outputs.manager_runtime == 'true'");
    expect(managerJob).not.toContain("if: needs.changes.outputs.runtime_lifecycle == 'true'");
    expect(managerJob).toContain('run: go vet ./...');
    expect(windowsJob).toContain(
      'go test ./internal/service/runtime ./internal/repository/setting ./internal/service/bootstrap'
    );
    expect(windowsJob).toContain("needs.changes.outputs.manager_runtime == 'true'");
    expect(windowsJob).not.toContain("needs.changes.outputs.runtime_lifecycle == 'true'");
    expect(windowsJob).toContain('run: go build ./cmd/cpa-manager-plus');
  });

  it('keeps bundle and Windows preflight validation as conditional required gates', () => {
    const workflow = readWorkflow('pr-check.yml');
    const scopeJob = jobBlock(workflow, 'changes');
    const managerJob = jobBlock(workflow, 'manager-server');
    const windowsJob = jobBlock(workflow, 'manager-server-windows-sqlite');

    for (const output of ['windows_preflight', 'bundle_boundary', 'manager_runtime', 'runtime_lifecycle']) {
      expect(scopeJob).toContain(`${output}: \${{ steps.classify.outputs.${output} }}`);
    }

    expect(managerJob).toContain("if: needs.changes.outputs.bundle_boundary == 'true'");
    expect(managerJob).toContain(
      'go test -race -count=1 -timeout 20m -json ./internal/service/maintenancebundle ./internal/command/managerdatabundle ./internal/processlock'
    );
    expect(windowsJob).toContain("needs.changes.outputs.windows_preflight == 'true'");
    expect(windowsJob).toContain("needs.changes.outputs.bundle_boundary == 'true'");
    expect(windowsJob).toContain(
      'go test -count=1 -timeout 20m -json ./internal/service/maintenancebundle ./internal/command/managerdatabundle'
    );
  });

  it('fails Required checks when required Runtime validation fails or is cancelled', () => {
    const requiredJob = jobBlock(readWorkflow('pr-check.yml'), 'required');
    const script = requiredJob.split('        run: |\n')[1].replace(/^ {10}/gm, '');
    const successResults = Object.fromEntries(
      [...requiredJob.matchAll(/^\s+([A-Z_]+_RESULT):/gm)].map(([, name]) => [name, 'success'])
    );

    for (const [resultName, checkName] of [
      ['RUNTIME_SUPERVISOR_RESULT', 'Runtime Supervisor'],
      ['RUNTIME_SUPERVISOR_WINDOWS_RESULT', 'Runtime Supervisor (Windows)'],
      ['DOCKER_BUILD_RESULT', 'Docker Build'],
    ]) {
      for (const result of ['success', 'skipped', 'failure', 'cancelled']) {
        const execution = spawnSync('bash', ['-c', script], {
          encoding: 'utf8',
          env: { ...process.env, ...successResults, [resultName]: result },
        });
        expect(execution.error).toBeUndefined();
        expect(execution.status).toBe(['success', 'skipped'].includes(result) ? 0 : 1);
        if (['failure', 'cancelled'].includes(result)) {
          expect(execution.stderr).toContain(
            `${checkName} did not complete successfully: ${result}`
          );
        }
      }
    }
  });

  it('keeps the existing main, dev, and v2 workflow triggers', () => {
    const workflow = readWorkflow('pr-check.yml');
    const trigger = workflow.slice(0, workflow.indexOf('\npermissions:'));
    for (const event of ['pull_request', 'push']) {
      expect(trigger).toContain(
        `  ${event}:\n    branches:\n      - main\n      - dev\n      - v2\n`
      );
    }
  });

  it('keeps release content validation inside the stable required-check aggregate', () => {
    const workflow = readWorkflow('pr-check.yml');
    const releaseJob = jobBlock(workflow, 'release-content');
    const requiredJob = jobBlock(workflow, 'required');

    expect(releaseJob).toContain('name: Release Content');
    expect(releaseJob).toContain('--changed-content --null');
    expect(releaseJob).toContain('--dry-run --allow-v2-beta');
    expect(releaseJob).toContain('--diff-filter=A -z');
    expect(requiredJob).toContain('- release-content');
    expect(requiredJob).toContain("RELEASE_CONTENT_RESULT: ${{ needs['release-content'].result }}");
    expect(requiredJob).toContain('"Release Content:${RELEASE_CONTENT_RESULT}"');
  });

  it('prevents metadata-action from moving the mutable latest tag', () => {
    for (const workflowName of ['release.yml', 'release-publish-recovery.yml']) {
      const workflow = readWorkflow(workflowName);
      expect(workflow).toContain('flavor: |\n            latest=false');
    }
  });

  it('runs repository-level release and installer tests from the root test entry', () => {
    const packageJson = JSON.parse(readFileSync(path.join(repoRoot, 'package.json'), 'utf8'));
    expect(packageJson.scripts.test).toContain('test:web');
    expect(packageJson.scripts.test).toContain('test:repo');
    expect(packageJson.scripts['test:repo']).toBe(
      'vitest run tests/*.test.mjs --exclude tests/nativeControlScripts.test.mjs'
    );
  });

  it('runs the Upgrade harness unit tests on every run, without Docker', () => {
    const workflow = readWorkflow('upgrade-docker.yml');
    const job = jobBlock(workflow, 'upgrade-docker');
    const nodeStep = stepBlock(job, 'Set up Node');
    const testStep = stepBlock(job, 'Test the Upgrade harness without Docker');

    expect(testStep).toContain('run: node --test bin/ci/upgrade-docker.test.mjs');
    expect(testStep).not.toMatch(/^\s+if:/m);
    expect(testStep).not.toContain('continue-on-error');
    expect(nodeStep).not.toMatch(/^\s+if:/m);
    expect(job.indexOf('- name: Set up Node')).toBeLessThan(
      job.indexOf('- name: Test the Upgrade harness without Docker')
    );
    expect(job.indexOf('- name: Test the Upgrade harness without Docker')).toBeLessThan(
      job.indexOf('- name: Classify the skip variable')
    );
  });

  it('runs the Panel Lite harness suite in the Frontend job', () => {
    const frontendJob = jobBlock(readWorkflow('pr-check.yml'), 'frontend');
    const testStep = stepBlock(frontendJob, 'Test Panel Lite acceptance harness');

    expect(testStep).toContain('run: node --test bin/ci/panel-lite-cpa-v8.test.mjs');
    expect(testStep).not.toMatch(/^\s+if:/m);
    expect(testStep).not.toContain('continue-on-error');
    expect(frontendJob.indexOf('- name: Test D harness lifecycle')).toBeLessThan(
      frontendJob.indexOf('- name: Test Panel Lite acceptance harness')
    );
  });

  it('uses NUL-delimited Git paths before classification and release validation', () => {
    const workflow = readWorkflow('pr-check.yml');

    expect(workflow).toContain('git diff --name-only --no-renames -z');
    expect(workflow).toContain('git show --pretty=format: --name-only --no-renames -z');
    expect(workflow).toContain('classify-pr-checks.mjs --null');
  });

  it('serializes every publishing stage behind release preflight', () => {
    const workflow = readWorkflow('release.yml');
    for (const jobName of [
      'build_release_assets',
      'inspect_github_release',
      'build_and_push_docker',
      'publish_github_release',
      'notify_telegram',
    ]) {
      const job = jobBlock(workflow, jobName);
      expect(
        /needs:\s*preflight|needs:[\s\S]*?\n\s+- preflight/.test(job),
        `${jobName} must depend on preflight`
      ).toBe(true);
    }
  });

  it('exposes a serialized dry-run path and rejects legacy release-note fallback', () => {
    const workflow = readWorkflow('release.yml');

    expect(workflow).toContain('workflow_dispatch:');
    expect(workflow).toContain('version:');
    expect(workflow).toContain('dry_run=true');
    expect(workflow).toContain(
      "import { parseReleaseTag } from './bin/release/validate-release.mjs'"
    );
    expect(workflow).toContain('group: release-publish');
    expect(workflow).toContain('cancel-in-progress: false');
    expect(workflow).not.toContain('Generate release notes');
    expect(workflow).not.toContain('git log --pretty');
    expect(workflow).not.toContain('previous_tag');
  });

  it.each([
    ['workflow_dispatch', 'refs/heads/main', 'v2.0.0-beta.1', 'true', true],
    ['workflow_dispatch', 'refs/heads/v2', 'v2.0.0-beta.1', 'true', true],
    ['workflow_dispatch', 'refs/heads/dev', 'v2.0.0-beta.1', 'true', false],
    ['workflow_dispatch', 'refs/heads/v2', 'v1.14.11', 'true', false],
    ['workflow_dispatch', 'refs/heads/main', 'v1.14.11', 'true', true],
    ['workflow_dispatch', 'refs/heads/main', 'v2.0.0-beta.1', 'false', false],
    ['workflow_dispatch', 'refs/heads/v2', 'v2.0.0-beta.1', 'false', false],
    ['push', 'refs/tags/v2.0.0-beta.1', 'v2.0.0-beta.1', '', false],
    ['workflow_dispatch', 'refs/heads/v2', 'v2.0.0', 'true', false],
    ['workflow_dispatch', 'refs/heads/v2', 'v2.0.0-rc.1', 'true', false],
  ])('enforces release context gates: %s %s %s dry=%s', (event, ref, tag, dryRun, accepted) => {
    const workflow = parseYaml(readWorkflow('release.yml'));
    const script = workflow.jobs.preflight.steps.find((step) => step.id === 'context').run;
    const scratch = mkdtempSync(path.join(tmpdir(), 'cpamp-release-context-'));
    try {
      const result = spawnSync('bash', ['-c', script], {
        cwd: repoRoot, encoding: 'utf8', env: { ...process.env,
          EVENT_NAME: event, EVENT_REF: ref, EVENT_REF_NAME: tag,
          INPUT_VERSION: tag, INPUT_DRY_RUN: dryRun, GITHUB_OUTPUT: path.join(scratch, 'output'),
        },
      });
      expect(result.status === 0, result.stderr).toBe(accepted);
      if (tag.startsWith('v2.') && (event === 'push' || dryRun === 'false'))
        expect(result.stderr).toContain('v2 real release publishing requires explicit maintainer gate unlock');
      if (accepted && tag.startsWith('v2.'))
        expect(readFileSync(path.join(scratch, 'output'), 'utf8')).toContain('release_major=2');
    } finally { rmSync(scratch, { recursive: true, force: true }); }
  });

  it('keeps the v2 candidate and six-asset rehearsal separate from the 1.x producers', () => {
    const workflow = parseYaml(readWorkflow('release.yml'));
    const jobs = workflow.jobs;
    const build = jobBlock(readWorkflow('release.yml'), 'build_release_assets');
    expect(build).toContain("PACKAGE_MODE: ${{ needs.preflight.outputs.release_major == '2' && 'v2' || 'v1' }}");
    expect(build).toContain('cpa-manager-plus-native-v2_${RELEASE_TAG}_linux_amd64.tar.gz');
    const docker = jobs.build_and_push_docker.steps;
    expect(docker.find((step) => step.name === 'Build and publish Manager Server image').if).toBe("env.RELEASE_MAJOR == '1'");
    const candidate = docker.find((step) => step.name === 'Build and verify Embedded OCI candidate without publication');
    expect(candidate.if).toBe("env.RELEASE_MAJOR == '2' && env.DRY_RUN == 'true'");
    for (const script of ['build-embedded-candidate.sh', 'verify-embedded-candidate.py', 'render-embedded-compose.mjs'])
      expect(candidate.run).toContain(script);
    expect(candidate.env.CPAMP_CANDIDATE_LOAD).toBe('0');
    expect(candidate.run).not.toMatch(/--push|docker push/);
    expect(jobs.package_release_assets.if).toBe("needs.preflight.outputs.release_major == '2' && needs.preflight.outputs.dry_run == 'true'");
    const pack = jobBlock(readWorkflow('release.yml'), 'package_release_assets');
    expect(pack).toContain('--mode assets');
    expect(pack).toContain('--dry-run --allow-v2-beta');
    expect(pack).toContain('--dry-run --allow-v2-beta-dry-run');
    expect(pack).toContain('versions.json');
    expect(jobs.publish_github_release.if).toBe("needs.preflight.outputs.dry_run != 'true'");
    expect(jobs.notify_telegram.if).toContain("needs.preflight.outputs.dry_run != 'true'");
    for (const step of jobs.inspect_github_release.steps.filter((step) => step.uses?.startsWith('softprops/')))
      expect(step.if).toContain("env.DRY_RUN != 'true'");
    // Walk every dependency, rejecting a cycle before it can strand artifacts.
    const visit = (name, ancestors = []) => {
      expect(ancestors, name).not.toContain(name);
      const needs = jobs[name].needs ?? [];
      for (const dependency of typeof needs === 'string' ? [needs] : needs) visit(dependency, [...ancestors, name]);
    };
    for (const name of Object.keys(jobs)) visit(name);
  });

  it('defaults manual update channel publication to a write-free dry-run', () => {
    const workflow = parseYaml(readWorkflow('update-index-publish.yml'));
    expect(workflow.on.workflow_dispatch.inputs.dry_run).toMatchObject({ type: 'boolean', default: true });
    expect(workflow.on.workflow_call.inputs.dry_run.default).toBe(false);
    const step = workflow.jobs.publish.steps.find((step) => step.name === 'Reconcile aliases and publish index last');
    expect(step.env.DRY_RUN).toBe('${{ inputs.dry_run }}');
    expect(step.run).toContain('if [ "${DRY_RUN}" = "true" ]; then args+=(--dry-run); fi');
    expect(step.run).toContain('publish-update-index.mjs "${args[@]}"');
  });

  it('scopes Telegram secrets to the delivery step', () => {
    const workflow = readWorkflow('release.yml');
    const notifyJob = jobBlock(workflow, 'notify_telegram');
    const stepsOffset = notifyJob.indexOf('\n    steps:');
    const jobConfiguration = notifyJob.slice(0, stepsOffset);
    const deliveryStep = notifyJob.slice(notifyJob.indexOf('- name: Send Telegram'));

    expect(jobConfiguration).not.toContain('TELEGRAM_BOT_TOKEN');
    expect(jobConfiguration).not.toContain('TELEGRAM_CHAT_ID');
    expect(deliveryStep).toContain('TELEGRAM_BOT_TOKEN: ${{ secrets.TELEGRAM_BOT_TOKEN }}');
    expect(deliveryStep).toContain('TELEGRAM_CHAT_ID: ${{ secrets.TELEGRAM_CHAT_ID }}');
  });

  it('keeps published Release reruns idempotent and fail-closed', () => {
    const workflow = readWorkflow('release.yml');
    const inspectJob = jobBlock(workflow, 'inspect_github_release');
    const dockerJob = jobBlock(workflow, 'build_and_push_docker');
    const publishJob = jobBlock(workflow, 'publish_github_release');

    expect(inspectJob).toContain('name: Resolve GitHub Release state');
    expect(inspectJob).toContain('bin/release/verify-published-release.mjs');
    expect(inspectJob).toContain('--allow-missing-assets "${allow_missing}"');
    expect(inspectJob).toContain('only missing assets will be uploaded');
    expect(inspectJob).toContain('matching_release_count');
    expect(inspectJob).toContain('multiple entries for ${RELEASE_TAG}');
    expect(dockerJob).toContain('- inspect_github_release');
    expect(publishJob).toContain('- inspect_github_release');
    expect(publishJob).toContain("if: needs.inspect_github_release.outputs.publish == 'true'");
    expect(workflow.indexOf('  inspect_github_release:')).toBeLessThan(
      workflow.indexOf('  build_and_push_docker:')
    );
    expect(inspectJob).toContain('overwrite_files: false');
  });

  it('uploads every new Release as a draft and publishes only after verification', () => {
    const workflow = readWorkflow('release.yml');
    const inspectJob = jobBlock(workflow, 'inspect_github_release');
    const publishJob = jobBlock(workflow, 'publish_github_release');

    expect(inspectJob).toContain('contents: write');
    expect(inspectJob).toContain('verification_mode=draft');
    expect(inspectJob).toContain('draft: true');
    expect(inspectJob).toContain('Resolve prepared GitHub Release id');
    expect(inspectJob).toContain('release_id: ${{ steps.prepared_release.outputs.release_id }}');
    expect(inspectJob).toContain('Verify prepared draft Release');
    expect(inspectJob).toContain('/releases/${RELEASE_ID}');
    expect(publishJob).toContain('Publish verified draft GitHub Release');
    expect(publishJob).toContain(
      "if: needs.inspect_github_release.outputs.publish == 'true' && needs.inspect_github_release.outputs.finalize == 'true'"
    );
    expect(publishJob).toContain('--request PATCH');
    expect(publishJob).toContain(
      'RELEASE_ID: ${{ needs.inspect_github_release.outputs.release_id }}'
    );
    expect(publishJob).toContain(
      '"${GITHUB_API_URL}/repos/${GITHUB_REPOSITORY}/releases/${RELEASE_ID}"'
    );
    expect(publishJob).not.toContain('/releases/tags/${RELEASE_TAG}');
    expect(publishJob).toContain('draft: false');
    expect(publishJob).toContain(
      'Verified draft GitHub Release published after all assets were uploaded.'
    );
    expect(inspectJob).toContain('echo "state=${verification_mode}"');
    expect(inspectJob).toContain('echo "state=missing"');
  });

  it('prevents automatic Telegram delivery on workflow reruns', () => {
    const workflow = readWorkflow('release.yml');
    const notifyJob = jobBlock(workflow, 'notify_telegram');

    expect(notifyJob).toContain('github.run_attempt == 1');
    expect(notifyJob).toContain('run: bash bin/release/send-telegram-release.sh');
    expect(telegramScript).not.toContain('--retry');
    expect(telegramScript).not.toContain('--retry-all-errors');
  });

  it('preserves false-valued GitHub API booleans during release recovery', () => {
    const workflow = readWorkflow('release-publish-recovery.yml');
    const booleanFilter = 'if type == "boolean" then tostring else empty end';

    expect(workflow).toContain(`.expired | ${booleanFilter}`);
    expect(workflow.match(new RegExp(`\\.draft \\| ${booleanFilter}`, 'g'))).toHaveLength(2);
    expect(workflow).not.toMatch(/\.(?:expired|draft)\s*\/\/\s*empty/);
  });

  it('allows release recovery to revalidate its prepared draft before publishing images', () => {
    const workflow = readWorkflow('release-publish-recovery.yml');
    const dockerJob = jobBlock(workflow, 'build_and_push_docker');
    const stepsOffset = dockerJob.indexOf('\n    steps:');
    const jobConfiguration = dockerJob.slice(0, stepsOffset);

    expect(jobConfiguration).toContain('contents: write');
    expect(jobConfiguration).toContain('packages: write');
    expect(dockerJob).toContain(
      'name: Revalidate prepared GitHub Release before registry mutation'
    );
    expect(dockerJob).toContain(
      '"${GITHUB_API_URL}/repos/${GITHUB_REPOSITORY}/releases/${RELEASE_ID}"'
    );
  });

  it('provides a validated explicit Telegram recovery workflow', () => {
    const workflow = readWorkflow('release-telegram-recovery.yml');
    const notifyJob = jobBlock(workflow, 'notify');
    const stepsOffset = notifyJob.indexOf('\n    steps:');
    const jobConfiguration = notifyJob.slice(0, stepsOffset);
    const deliveryStep = notifyJob.slice(notifyJob.indexOf('- name: Send Telegram'));

    expect(workflow).toContain('workflow_dispatch:');
    expect(workflow).toContain('confirm_resend:');
    expect(workflow).toContain('type: boolean');
    expect(notifyJob).toContain('refs/heads/main');
    expect(notifyJob).toContain('name: Validate tagged release provenance');
    expect(notifyJob).not.toContain('git checkout --detach');
    expect(notifyJob).toContain('git merge-base --is-ancestor "${RELEASE_SHA}" origin/main');
    expect(notifyJob).toContain('git merge-base --is-ancestor "${release_dev_sha}" origin/dev');
    expect(notifyJob).toContain('--main-ref "${RELEASE_SHA}"');
    expect(notifyJob).toContain('--dev-ref "${release_dev_sha}"');
    expect(notifyJob).toContain('git show "${release_sha}:docs/release-posts/');
    expect(notifyJob).toContain('--mode metadata');
    expect(jobConfiguration).not.toContain('TELEGRAM_BOT_TOKEN');
    expect(jobConfiguration).not.toContain('TELEGRAM_CHAT_ID');
    expect(deliveryStep).toContain("TELEGRAM_STRICT: 'true'");
    expect(deliveryStep).toContain(
      'RELEASE_POST_PATH: ${{ steps.release_source.outputs.release_post }}'
    );
    expect(deliveryStep).toContain('run: bash bin/release/send-telegram-release.sh');
  });

  it('does not retain the main-only standalone Demo and Docs workflow', () => {
    expect(existsSync(path.join(workflowDir, 'demo-docs-check.yml'))).toBe(false);
  });

  it('keeps GitHub Actions dependency updates on the integration branch', () => {
    expect(dependabotConfig).toContain('package-ecosystem: github-actions');
    expect(dependabotConfig).toContain('target-branch: dev');
    expect(dependabotConfig).toContain('interval: weekly');
  });
});
