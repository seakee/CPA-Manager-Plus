import { existsSync, lstatSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const CHECK_NAMES = [
  'frontend',
  'manager_server',
  'runtime_supervisor',
  'ingress',
  'windows_sqlite',
  'windows_preflight',
  'bundle_boundary',
  'manager_runtime',
  'runtime_lifecycle',
  'native_control',
  'docker',
  'docker_all_images',
  'release_content',
];

const FORBIDDEN_INVISIBLE_CODE_POINTS = new Set([
  0x200b, 0x200c, 0x200d, 0x200e, 0x200f, 0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2060, 0x2066,
  0x2067, 0x2068, 0x2069, 0xfeff,
]);

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');

const normalizePath = (filePath) =>
  filePath.replace(/\r$/, '').replaceAll('\\', '/').replace(/^\.\//, '');

const normalizeChangedFiles = (changedFiles) => [
  ...new Set(changedFiles.map(normalizePath).filter(Boolean)),
];

const startsWithPath = (filePath, directory) => filePath.startsWith(`${directory}/`);

const allChecks = (enabled) =>
  Object.fromEntries(CHECK_NAMES.map((checkName) => [checkName, enabled]));

const triggersAllChecks = (filePath) =>
  filePath === '.github/workflows/pr-check.yml' ||
  filePath === 'bin/ci/classify-pr-checks.mjs' ||
  // The shard planner decides which Go tests each race shard runs, so a change
  // there can affect any sharded gate and must schedule every check.
  filePath === 'bin/ci/go-test-shard.mjs' ||
  filePath === 'tests/goTestShard.test.mjs' ||
  filePath === 'tests/prCheckClassifier.test.mjs';

const triggersFrontend = (filePath) =>
  startsWithPath(filePath, '.github/workflows') ||
  startsWithPath(filePath, 'apps/web') ||
  startsWithPath(filePath, 'tests') ||
  filePath === 'README.md' ||
  filePath === 'README_CN.md' ||
  filePath === 'package.json' ||
  filePath === 'package-lock.json' ||
  // The legacy Pages redirect site is covered by tests/pagesRedirect.test.mjs,
  // which the Frontend job runs: a redirect edit must schedule it.
  startsWithPath(filePath, 'bin/pages-redirect') ||
  // The toolchain baseline assertions live in tests/ciWorkflowIntegrity.test.mjs,
  // which the Frontend job runs: the Node baseline, every image base and every
  // Go module manifest that carries the Go directive.
  filePath === '.nvmrc' ||
  filePath === 'Dockerfile' ||
  filePath === 'apps/manager-server/go.mod' ||
  filePath === 'apps/ingress/go.mod' ||
  filePath === 'apps/runtime-supervisor/go.mod' ||
  filePath === '.github/dependabot.yml' ||
  filePath === 'eslint.config.js' ||
  filePath === 'bin/install-cpamp.sh' ||
  filePath === 'bin/ci/validate-cpa-v8-evidence.mjs' ||
  filePath === 'bin/ci/run-cpa-v8-qualification.mjs' ||
  filePath === 'bin/ci/prepare-cpa-v8-artifact.py' ||
  filePath === 'bin/ci/cpa-v8-external-fixtures.mjs' ||
  filePath === 'bin/ci/cpa-v8-usage-fixtures.mjs' ||
  filePath === 'bin/ci/cpa-v8-stage-fixtures.mjs' ||
  filePath === 'bin/ci/cpa-v8-extension-fixtures.mjs' ||
  // The D acceptance and its native `node:test` lifecycle suite run in the
  // Frontend job, which is the job that runs the repository's Node tests.
  filePath === 'bin/ci/fresh-adopt-network.mjs' ||
  filePath === 'bin/ci/fresh-adopt-lifecycle.test.mjs' ||
  filePath === 'bin/ci/fresh-adopt-redaction.mjs' ||
  // The Panel Lite acceptance and its native `node:test` suite run in the
  // Frontend job as well.
  filePath === 'bin/ci/panel-lite-cpa-v8.mjs' ||
  filePath === 'bin/ci/panel-lite-cpa-v8.test.mjs' ||
  // The Gate B/C readiness evidence harness and its native `node:test` suite run
  // in the Frontend job; the report that quotes its observations is part of the
  // same change and must keep that job scheduled.
  filePath === 'bin/ci/readiness-gate-bc.mjs' ||
  filePath === 'bin/ci/readiness-gate-bc.test.mjs' ||
  // The design-token checker, its committed baseline and its runtime property
  // registry run in the Frontend job alongside tests/designTokenCheck.test.mjs.
  filePath === 'bin/ci/check-design-tokens.mjs' ||
  filePath === 'bin/ci/design-token-baseline.json' ||
  filePath === 'bin/ci/design-token-runtime-properties.json' ||
  startsWithPath(filePath, 'docs/architecture/evidence') ||
  startsWithPath(filePath, 'bin/release');

const triggersManagerServer = (filePath) =>
  startsWithPath(filePath, 'apps/manager-server') || filePath === 'bin/release/package-native.sh';

const triggersRuntimeSupervisor = (filePath) =>
  startsWithPath(filePath, 'apps/runtime-supervisor') ||
  startsWithPath(filePath, 'docker/runtime') ||
  filePath === 'Dockerfile' ||
  startsWithPath(filePath, 'bin/ci/runtime-boundary');

const triggersIngress = (filePath) =>
  startsWithPath(filePath, 'apps/ingress') || filePath === 'Dockerfile';

const managerServerModuleFile = (filePath, relativePath) =>
  startsWithPath(filePath, `apps/manager-server/${relativePath}`);

const managerServerDependencyFile = (filePath) =>
  filePath === 'apps/manager-server/go.mod' || filePath === 'apps/manager-server/go.sum';

const triggersWindowsSQLite = (filePath) =>
  managerServerModuleFile(filePath, 'internal/repository/sqlite') ||
  managerServerModuleFile(filePath, 'internal/adapters/sqlite') ||
  managerServerDependencyFile(filePath);

const triggersWindowsPreflight = (filePath) =>
  managerServerModuleFile(filePath, 'cmd/cpa-manager-plus') ||
  managerServerModuleFile(filePath, 'internal/processlock') ||
  managerServerModuleFile(filePath, 'internal/config') ||
  managerServerModuleFile(filePath, 'internal/service/legacypreflight') ||
  managerServerModuleFile(filePath, 'internal/command/legacypreflight') ||
  triggersWindowsSQLite(filePath) ||
  managerServerDependencyFile(filePath) ||
  filePath === 'bin/release/package-native.sh';

const triggersBundleBoundary = (filePath) =>
  managerServerModuleFile(filePath, 'internal/service/maintenancebundle') ||
  managerServerModuleFile(filePath, 'internal/command/managerdatabundle') ||
  managerServerModuleFile(filePath, 'internal/processlock') ||
  managerServerModuleFile(filePath, 'internal/service/legacyartifact') ||
  managerServerModuleFile(filePath, 'internal/command/legacyartifact') ||
  triggersWindowsSQLite(filePath) ||
  managerServerDependencyFile(filePath);

const triggersManagerRuntime = (filePath) =>
  filePath === 'apps/manager-server/cmd/cpa-manager-plus/main.go' ||
  filePath === 'apps/manager-server/internal/app/app.go' ||
  filePath === 'apps/manager-server/internal/config/config.go' ||
  filePath === 'apps/manager-server/internal/model/runtime.go' ||
  filePath === 'apps/manager-server/internal/http/router/router.go' ||
  managerServerModuleFile(filePath, 'internal/http/controller/cpaupdate') ||
  managerServerModuleFile(filePath, 'internal/command/runtimeconfig') ||
  managerServerModuleFile(filePath, 'internal/service/cpaupdate') ||
  managerServerModuleFile(filePath, 'internal/service/runtime') ||
  managerServerModuleFile(filePath, 'internal/repository/setting') ||
  managerServerModuleFile(filePath, 'internal/service/bootstrap') ||
  filePath === 'apps/manager-server/internal/store/store.go' ||
  managerServerDependencyFile(filePath);

// The Fresh wizard owns the Manager half of the Runtime lifecycle: it seeds the
// durable desired state, submits every Start and Stop, derives the reconcile
// obligation of an incomplete installation, and fences the artifact an automatic
// restart may launch. Those files live in the command package, so they are named
// here rather than being implied by a module path.
const triggersFreshRuntimeLifecycle = (filePath) =>
  filePath.startsWith('apps/manager-server/cmd/cpa-manager-plus/fresh');

const triggersRuntimeLifecycle = (filePath) =>
  triggersManagerRuntime(filePath) ||
  triggersFreshRuntimeLifecycle(filePath) ||
  startsWithPath(filePath, 'apps/runtime-supervisor') ||
  startsWithPath(filePath, 'apps/ingress') ||
  startsWithPath(filePath, 'docker/runtime') ||
  filePath === 'bin/ci/runtime-v8-default-smoke.py' ||
  filePath === 'bin/ci/runtime-slim-acquire-smoke.py' ||
  filePath === 'bin/ci/runtime-v7-compatibility.compose.yml' ||
  filePath.startsWith('bin/ci/runtime12-') ||
  filePath.startsWith('bin/ci/runtime14-') ||
  filePath.startsWith('bin/ci/runtime17-') ||
  filePath.startsWith('bin/ci/runtime18-') ||
  filePath.startsWith('bin/ci/runtime19-') ||
  filePath === 'bin/ci/validate-runtime12-compose.mjs' ||
  filePath === 'Dockerfile' ||
  filePath === 'docker-compose.yml' ||
  filePath === 'docker-compose.manager.yml';

const triggersNativeControl = (filePath) =>
  startsWithPath(filePath, 'bin/native') ||
  filePath === 'bin/release/package-native.sh' ||
  // Native packaging consumes the same license collector and evidence
  // generator as the images, so any change there must rerun its tests.
  filePath === 'bin/release/collect-third-party-licenses.py' ||
  filePath === 'bin/release/image-evidence.py' ||
  filePath === 'bin/release/hash-fullweb.mjs' ||
  filePath === 'bin/release/embedded-third-party-inventory.txt' ||
  startsWithPath(filePath, 'bin/release/third-party-license-sources') ||
  filePath === 'tests/nativeControlScripts.test.mjs' ||
  filePath === 'package.json' ||
  filePath === 'package-lock.json';

const triggersDocker = (filePath) =>
  startsWithPath(filePath, 'apps/web') ||
  startsWithPath(filePath, 'apps/manager-server') ||
  startsWithPath(filePath, 'apps/runtime-supervisor') ||
  startsWithPath(filePath, 'apps/ingress') ||
  startsWithPath(filePath, 'docker') ||
  filePath === 'bin/ci/runtime-v8-default-smoke.py' ||
  filePath === 'bin/ci/runtime-slim-acquire-smoke.py' ||
  filePath === 'bin/ci/runtime-v7-compatibility.compose.yml' ||
  filePath.startsWith('bin/ci/runtime12-') ||
  filePath.startsWith('bin/ci/runtime14-') ||
  filePath.startsWith('bin/ci/runtime17-') ||
  filePath.startsWith('bin/ci/runtime18-') ||
  filePath.startsWith('bin/ci/runtime19-') ||
  filePath === 'bin/ci/validate-runtime12-compose.mjs' ||
  filePath === 'Dockerfile' ||
  filePath === 'docker-compose.yml' ||
  filePath === 'docker-compose.manager.yml' ||
  filePath === '.dockerignore' ||
  filePath === 'package.json' ||
  filePath === 'package-lock.json';

// Runtime E2E requires all images; shared Docker inputs must also build all images.
const triggersDockerAllImages = (filePath) =>
  triggersRuntimeLifecycle(filePath) ||
  startsWithPath(filePath, 'docker') ||
  filePath === '.dockerignore';

const triggersReleaseContent = (filePath) =>
  startsWithPath(filePath, 'docs/release-notes') ||
  startsWithPath(filePath, 'docs/release-posts') ||
  filePath === 'bin/release/validate-release.mjs';

export const classifyChangedFiles = (changedFiles) => {
  const files = normalizeChangedFiles(changedFiles);
  if (files.length === 0 || files.some(triggersAllChecks)) return allChecks(true);

  const frontend = files.some(triggersFrontend);
  const managerServer = files.some(triggersManagerServer);

  return {
    frontend,
    manager_server: managerServer,
    runtime_supervisor: files.some(triggersRuntimeSupervisor),
    ingress: files.some(triggersIngress),
    windows_sqlite: files.some(triggersWindowsSQLite),
    windows_preflight: files.some(triggersWindowsPreflight),
    bundle_boundary: files.some(triggersBundleBoundary),
    manager_runtime: files.some(triggersManagerRuntime),
    runtime_lifecycle: files.some(triggersRuntimeLifecycle),
    native_control: files.some(triggersNativeControl),
    docker: files.some(triggersDocker),
    docker_all_images: files.some(triggersDockerAllImages),
    release_content: files.some(triggersReleaseContent),
  };
};

export const parseChangedFilesInput = (input, { nullDelimited = false } = {}) =>
  input.split(nullDelimited ? '\0' : /\r?\n/).filter(Boolean);

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

export const scanChangedTextFiles = (changedFiles, { root = repoRoot } = {}) => {
  const violations = [];

  for (const filePath of normalizeChangedFiles(changedFiles)) {
    const absolutePath = path.resolve(root, filePath);
    if (!absolutePath.startsWith(`${root}${path.sep}`)) {
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
  const argumentsList = process.argv.slice(2);
  const unknownArguments = argumentsList.filter((argument) => argument !== '--null');
  if (unknownArguments.length > 0) {
    console.error(`Unknown classifier arguments: ${unknownArguments.join(', ')}`);
    process.exitCode = 1;
    return;
  }

  const changedFiles = parseChangedFilesInput(readFileSync(0, 'utf8'), {
    nullDelimited: argumentsList.includes('--null'),
  });
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
