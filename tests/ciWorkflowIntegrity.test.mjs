import { existsSync, readdirSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const workflowDir = path.join(repoRoot, '.github', 'workflows');
const readText = (filePath) => readFileSync(filePath, 'utf8').replace(/\r\n?/g, '\n');
const readWorkflow = (name) => readText(path.join(workflowDir, name));
const dependabotConfig = readText(path.join(repoRoot, '.github', 'dependabot.yml'));
const telegramScript = readFileSync(
  path.join(repoRoot, 'bin', 'release', 'send-telegram-release.sh'),
  'utf8'
).replace(/\r\n?/g, '\n');

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

describe('GitHub Actions workflow integrity', () => {
  it('resolves the web version without Unix-only shell syntax', () => {
    const viteConfig = readText(path.join(repoRoot, 'apps', 'web', 'vite.config.ts'));

    expect(viteConfig).toContain("execFileSync('git', args");
    expect(viteConfig).not.toContain('2>/dev/null');
    expect(viteConfig).not.toContain('|| echo');
  });

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

  it('keeps release content validation inside the stable required-check aggregate', () => {
    const workflow = readWorkflow('pr-check.yml');
    const releaseJob = jobBlock(workflow, 'release-content');
    const requiredJob = jobBlock(workflow, 'required');

    expect(releaseJob).toContain('name: Release Content');
    expect(releaseJob).toContain('--changed-content --null');
    expect(releaseJob).toContain('--diff-filter=A -z');
    expect(requiredJob).toContain('- release-content');
    expect(requiredJob).toContain("RELEASE_CONTENT_RESULT: ${{ needs['release-content'].result }}");
    expect(requiredJob).toContain('"Release Content:${RELEASE_CONTENT_RESULT}"');
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
