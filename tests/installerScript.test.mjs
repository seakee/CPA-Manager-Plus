import { spawn, spawnSync } from 'node:child_process';
import {
  chmodSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  realpathSync,
  readdirSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const installerPath = path.join(repoRoot, 'bin/install-cpamp.sh');

const combinedOutput = (result) => `${result.stdout}\n${result.stderr}`;

const runInstaller = (env) =>
  spawnSync('bash', [installerPath], {
    cwd: repoRoot,
    env: {
      ...process.env,
      CPAMP_DRY_RUN: '1',
      CPAMP_NON_INTERACTIVE: '1',
      CPAMP_LANG: 'en-US',
      CPAMP_INSTALL_DIR: '/tmp/cpamp-installer-test',
      ...env,
    },
    encoding: 'utf8',
  });

const runInstallerFromStdin = (env) =>
  spawnSync('bash', ['-c', `bash < ${JSON.stringify(installerPath)}`], {
    cwd: repoRoot,
    env: {
      ...process.env,
      CPAMP_DRY_RUN: '1',
      CPAMP_INSTALL_DIR: '/tmp/cpamp-installer-test',
      ...env,
    },
    encoding: 'utf8',
  });

const writeFakeDocker = (dir) => {
  const fakeDocker = path.join(dir, 'docker');
  writeFileSync(
    fakeDocker,
    `#!/usr/bin/env bash
set -eu
if [ -n "\${FAKE_DOCKER_LOG:-}" ]; then
  printf '%s|%s\n' "\${COMPOSE_PROJECT_NAME:-}" "$*" >> "$FAKE_DOCKER_LOG"
fi
if [ "$1" = "volume" ] && [ "\${2:-}" = "inspect" ]; then
  if [ "\${FAKE_DOCKER_VOLUME_EXISTS:-0}" = "1" ]; then
    exit 0
  fi
  exit 1
fi
if [ "$1" = "info" ] && [ "\${FAKE_DOCKER_DAEMON_OK:-1}" != "1" ]; then
  exit 1
fi
if [ "$1" = "compose" ] && [ "\${2:-}" = "exec" ]; then
  case "$*" in
    *'/v0/management/config'*)
      if [ "\${FAKE_DOCKER_CPA_OK:-1}" = "1" ]; then
        exit 0
      fi
      exit 1
      ;;
    *'/status'*)
      if [ "\${FAKE_DOCKER_AUTH_OK:-1}" = "1" ]; then
        exit 0
      fi
      exit 1
      ;;
    *) exit 0 ;;
  esac
fi
if [ "$1" = "compose" ] && [ "\${2:-}" = "run" ]; then
  case "$*" in
    *'manager-data-snapshot create'*)
      if [ "\${FAKE_DOCKER_SNAPSHOT_OK:-1}" != "1" ]; then
        exit 1
      fi
      if [ -n "\${FAKE_DOCKER_SNAPSHOT_STORE:-}" ]; then
        mkdir -p "$FAKE_DOCKER_SNAPSHOT_STORE"
        for suffix in '' -wal -shm -journal; do
          if [ -e "\${FAKE_DOCKER_DB_PATH:-}$suffix" ]; then
            cp "\${FAKE_DOCKER_DB_PATH}$suffix" "$FAKE_DOCKER_SNAPSHOT_STORE/database$suffix"
          else
            : > "$FAKE_DOCKER_SNAPSHOT_STORE/database$suffix.missing"
          fi
        done
        if [ -e "\${FAKE_DOCKER_DATA_KEY_PATH:-}" ]; then
          cp "$FAKE_DOCKER_DATA_KEY_PATH" "$FAKE_DOCKER_SNAPSHOT_STORE/data.key"
        else
          : > "$FAKE_DOCKER_SNAPSHOT_STORE/data.key.missing"
        fi
      fi
      ;;
    *'manager-data-snapshot restore'*)
      if [ "\${FAKE_DOCKER_RESTORE_OK:-1}" != "1" ]; then
        exit 1
      fi
      if [ -n "\${FAKE_DOCKER_SNAPSHOT_STORE:-}" ]; then
        for suffix in '' -wal -shm -journal; do
          if [ -e "$FAKE_DOCKER_SNAPSHOT_STORE/database$suffix.missing" ]; then
            rm -f "\${FAKE_DOCKER_DB_PATH}$suffix"
          else
            cp "$FAKE_DOCKER_SNAPSHOT_STORE/database$suffix" "\${FAKE_DOCKER_DB_PATH}$suffix"
          fi
        done
        if [ -e "$FAKE_DOCKER_SNAPSHOT_STORE/data.key.missing" ]; then
          rm -f "$FAKE_DOCKER_DATA_KEY_PATH"
        else
          cp "$FAKE_DOCKER_SNAPSHOT_STORE/data.key" "$FAKE_DOCKER_DATA_KEY_PATH"
        fi
      fi
      ;;
    *'manager-data-snapshot delete'*)
      if [ "\${FAKE_DOCKER_SNAPSHOT_DELETE_OK:-1}" != "1" ]; then
        exit 1
      fi
      if [ -n "\${FAKE_DOCKER_SNAPSHOT_STORE:-}" ]; then
        rm -f "$FAKE_DOCKER_SNAPSHOT_STORE"/*
        rmdir "$FAKE_DOCKER_SNAPSHOT_STORE"
      fi
      ;;
    *'store-cpa-connection'*)
      if [ -n "\${FAKE_DOCKER_DB_PATH:-}" ]; then
        printf 'import-attempt\n' >> "$FAKE_DOCKER_DB_PATH"
      fi
      if [ "\${FAKE_DOCKER_MUTATE_ALL_DATA:-0}" = "1" ]; then
        for suffix in -wal -shm -journal; do
          printf 'import-attempt\n' >> "\${FAKE_DOCKER_DB_PATH}$suffix"
        done
        printf 'import-attempt\n' >> "$FAKE_DOCKER_DATA_KEY_PATH"
      fi
      if [ -n "\${FAKE_DOCKER_IMPORTED_KEY_LOG:-}" ]; then
        for argument in "$@"; do
          case "$argument" in
            *:/run/cpamp-import/cpa-management-key:ro)
              key_file="\${argument%:/run/cpamp-import/cpa-management-key:ro}"
              cp "$key_file" "$FAKE_DOCKER_IMPORTED_KEY_LOG"
              ;;
          esac
        done
      fi
      if [ "\${FAKE_DOCKER_IMPORT_OK:-1}" != "1" ]; then
        exit 1
      fi
      ;;
  esac
fi
exit 0
`
  );
  chmodSync(fakeDocker, 0o755);
  return fakeDocker;
};

const nativePlatform = process.platform === 'darwin' ? 'darwin' : 'linux';
const nativeArch = process.arch === 'arm64' ? 'arm64' : 'amd64';

const writeLegacyNativeInstall = (installDir) => {
  const packageName = `cpa-manager-plus_vold_${nativePlatform}_${nativeArch}`;
  const binaryDir = path.join(installDir, 'runtime', packageName);
  const configPath = path.join(binaryDir, 'config.json');
  const runPath = path.join(installDir, 'run.sh');
  const adminKeyPath = path.join(installDir, 'secrets', 'cpamp-admin-key');
  const cpaKeyPath = path.join(installDir, 'secrets', 'cpa-management-key');
  const dbPath = path.join(installDir, 'data', 'usage.sqlite');
  const dataKeyPath = path.join(installDir, 'data', 'data.key');

  mkdirSync(binaryDir, { recursive: true });
  mkdirSync(path.dirname(adminKeyPath), { recursive: true });
  mkdirSync(path.dirname(dbPath), { recursive: true });
  writeFileSync(
    path.join(binaryDir, 'cpa-manager-plus'),
    `#!/usr/bin/env bash
if [ "\${FAKE_LEGACY_NATIVE_IGNORE_TERM:-0}" = "1" ]; then
  trap ':' TERM INT
else
  trap 'exit 0' TERM INT
fi
while true; do sleep 1; done
`
  );
  chmodSync(path.join(binaryDir, 'cpa-manager-plus'), 0o755);
  writeFileSync(
    configPath,
    `${JSON.stringify(
      {
        httpAddr: '0.0.0.0:18317',
        dataDir: '../../data',
        adminKeyFile: '../../secrets/cpamp-admin-key',
        dataKeyPath: '../../data/data.key',
        cpaUpstreamUrl: 'http://127.0.0.1:8317',
        managementKeyFile: '../../secrets/cpa-management-key',
        collectorMode: 'http',
        queue: 'legacy-usage',
        popSide: 'left',
        batchSize: 321,
        pollIntervalMs: 750,
        queryLimit: 65432,
      },
      null,
      2
    )}\n`
  );
  writeFileSync(
    runPath,
    `#!/usr/bin/env bash\nset -euo pipefail\ncd "${binaryDir}"\nexec ./cpa-manager-plus\n`
  );
  chmodSync(runPath, 0o755);
  writeFileSync(adminKeyPath, 'cpamp_existing_admin_key\n');
  chmodSync(adminKeyPath, 0o600);
  writeFileSync(cpaKeyPath, 'cpa_existing_management_key\n');
  chmodSync(cpaKeyPath, 0o600);
  writeFileSync(dbPath, 'existing-usage-data\n');
  writeFileSync(dataKeyPath, 'existing-data-key\n');
  writeFileSync(
    path.join(installDir, 'cpa-manager-plus.service'),
    `[Service]\nWorkingDirectory=${binaryDir}\nExecStart=${binaryDir}/cpa-manager-plus\n`
  );

  return {
    binaryDir,
    configPath,
    runPath,
    adminKeyPath,
    cpaKeyPath,
    dbPath,
    dataKeyPath,
  };
};

const writeFakeNativeRelease = () => {
  const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
  const fixtureDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-fixture-'));
  const packageName = `cpa-manager-plus_vnext_${nativePlatform}_${nativeArch}`;
  const packageDir = path.join(fixtureDir, packageName);
  const archivePath = path.join(fixtureDir, `${packageName}.tar.gz`);
  mkdirSync(packageDir, { recursive: true });
  writeFileSync(
    path.join(packageDir, 'cpa-manager-plus'),
    `#!/usr/bin/env bash
set -euo pipefail
if [ "\${1:-}" = "store-cpa-connection" ]; then
  printf '%s\n' "$*" >> "$FAKE_NATIVE_COMMAND_LOG"
  printf 'import-attempt\n' >> "$FAKE_NATIVE_DB_PATH"
  if [ -n "\${FAKE_NATIVE_IMPORT_MARKER:-}" ]; then
    : > "$FAKE_NATIVE_IMPORT_MARKER"
  fi
  if [ -n "\${FAKE_NATIVE_JOURNAL_PATH:-}" ]; then
    printf 'import-attempt\n' >> "$FAKE_NATIVE_JOURNAL_PATH"
  fi
if [ "\${FAKE_NATIVE_IMPORT_OK:-1}" != "1" ]; then
    exit 41
  fi
  exit 0
fi
if [ "\${1:-}" = "sanitize-runtime-config" ]; then
  shift
  input=""
  output=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --input) input="$2"; shift 2 ;;
      --output) output="$2"; shift 2 ;;
      *) exit 42 ;;
    esac
  done
  node -e 'const fs=require("fs"); const value=JSON.parse(fs.readFileSync(process.argv[1], "utf8")); delete value.cpaUpstreamUrl; delete value.managementKeyFile; fs.writeFileSync(process.argv[2], JSON.stringify(value, null, 2)+"\\n");' "$input" "$output"
  exit 0
fi
if [ "\${FAKE_NATIVE_IGNORE_TERM:-0}" = "1" ]; then
  trap ':' TERM INT
else
  trap 'exit 0' TERM INT
fi
while true; do
  sleep 1
done
`
  );
  chmodSync(path.join(packageDir, 'cpa-manager-plus'), 0o755);
  const tarResult = spawnSync('tar', ['-czf', archivePath, '-C', fixtureDir, packageName], {
    cwd: repoRoot,
    encoding: 'utf8',
  });
  expect(tarResult.status).toBe(0);

  const fakeCurl = path.join(fakeBin, 'curl');
  writeFileSync(
    fakeCurl,
    `#!/usr/bin/env bash
set -euo pipefail
for arg in "$@"; do
  case "$arg" in
    */health)
      [ "\${FAKE_NATIVE_HEALTH_OK:-1}" = "1" ]
      exit
      ;;
    */status)
      [ "\${FAKE_NATIVE_AUTH_OK:-1}" = "1" ]
      exit
      ;;
    */v0/management/config)
      [ "\${FAKE_NATIVE_CPA_OK:-1}" = "1" ]
      exit
      ;;
  esac
done
out=""
previous=""
for arg in "$@"; do
  if [ "$previous" = "-o" ]; then
    out="$arg"
    break
  fi
  previous="$arg"
done
if [ -n "$out" ]; then
  cp "$CPAMP_FAKE_NATIVE_ARCHIVE" "$out"
  exit 0
fi
exit 22
`
  );
  chmodSync(fakeCurl, 0o755);
  const realLsof = ['/usr/sbin/lsof', '/usr/bin/lsof', '/sbin/lsof'].find((candidate) =>
    existsSync(candidate)
  );
  const fakeLsof = path.join(fakeBin, 'lsof');
  writeFileSync(
    fakeLsof,
    `#!/usr/bin/env bash
set -euo pipefail
case "$*" in
  *-iTCP*)
    # A host port published by the local Docker daemon (for example 18317) is
    # environmental noise for these tests; the native listener gate must see a
    # free port. Non-network queries fall through to the real lsof.
    exit 1
    ;;
esac
${realLsof ? `exec ${realLsof} "$@"` : 'exit 1'}
`
  );
  chmodSync(fakeLsof, 0o755);
  return { fakeBin, fixtureDir, archivePath, packageName };
};

const stopNativeFixtureProcess = (installDir) => {
  const pidPath = path.join(installDir, 'cpa-manager-plus.pid');
  if (!existsSync(pidPath)) return;
  const pid = Number(readFileSync(pidPath, 'utf8').trim());
  if (!Number.isInteger(pid) || pid <= 0) return;
  try {
    process.kill(pid, 'SIGTERM');
  } catch {
    // The fixture process may already have exited during rollback.
  }
  try {
    process.kill(pid, 0);
    process.kill(pid, 'SIGKILL');
  } catch {
    // The fixture process has exited or is not reachable anymore.
  }
};

describe('installer script', () => {
  it('passes shell syntax validation', () => {
    const result = spawnSync('bash', ['-n', installerPath], {
      cwd: repoRoot,
      encoding: 'utf8',
    });

    expect(result.status).toBe(0);
    expect(result.stderr).toBe('');
  });

  it('refuses interactive execution when stdin is not a terminal', () => {
    const result = runInstallerFromStdin({});

    expect(result.status).toBe(1);
    expect(combinedOutput(result)).toContain('Interactive install requires a terminal on stdin');
  });

  it('keeps explicit non-interactive stdin execution available', () => {
    const result = runInstallerFromStdin({
      CPAMP_NON_INTERACTIVE: '1',
      CPAMP_LANG: 'en-US',
      CPAMP_INSTALL_MODE: 'stack',
      CPAMP_DEPLOY_METHOD: 'docker',
    });

    expect(result.status).toBe(0);
    expect(result.stdout).toContain('Install scope: CPA + CPAMP stack');
    expect(result.stdout).toContain('docker compose pull');
  });

  it('prints a full Docker stack dry-run plan', () => {
    const result = runInstaller({
      CPAMP_INSTALL_MODE: 'stack',
      CPAMP_DEPLOY_METHOD: 'docker',
    });

    expect(result.status).toBe(0);
    expect(result.stdout).toContain('Install scope: CPA + CPAMP stack');
    expect(result.stdout).toContain('CPA URL for CPAMP: http://cli-proxy-api:8317');
    expect(result.stdout).toContain('docker compose pull');
    expect(result.stdout).toContain('Dry-run plan completed');
  });

  it('keeps CPAMP-only non-interactive installs in first-setup mode by default', () => {
    const result = runInstaller({
      CPAMP_INSTALL_MODE: 'cpamp',
      CPAMP_DEPLOY_METHOD: 'docker',
    });

    expect(result.status).toBe(0);
    expect(result.stdout).toContain('CPA connection: enter during first setup');
    expect(result.stdout).toContain('Dry-run plan completed');
  });

  it('rejects native full stack installs', () => {
    const result = runInstaller({
      CPAMP_INSTALL_MODE: 'stack',
      CPAMP_DEPLOY_METHOD: 'native',
    });
    const output = `${result.stdout}\n${result.stderr}`;

    expect(result.status).toBe(1);
    expect(output).toContain('Native stack install is not supported yet');
  });

  it('rejects CPA URLs that would inject extra env lines', () => {
    const result = runInstaller({
      CPAMP_INSTALL_MODE: 'cpamp',
      CPAMP_DEPLOY_METHOD: 'docker',
      CPAMP_CPA_CONNECTION_MODE: 'env',
      CPAMP_CPA_URL: 'http://host.docker.internal:8317\nCPA_MANAGER_ADMIN_KEY=bad',
      CPAMP_CPA_MANAGEMENT_KEY: 'cpa_existing_management_key',
    });

    expect(result.status).toBe(1);
    expect(combinedOutput(result)).toContain('CPA URL must be a single line');
  });

  it('rejects CPA URLs with URL fragments', () => {
    const result = runInstaller({
      CPAMP_INSTALL_MODE: 'cpamp',
      CPAMP_DEPLOY_METHOD: 'docker',
      CPAMP_CPA_CONNECTION_MODE: 'env',
      CPAMP_CPA_URL: 'http://host.docker.internal:8317#fragment',
      CPAMP_CPA_MANAGEMENT_KEY: 'cpa_existing_management_key',
    });

    expect(result.status).toBe(1);
    expect(combinedOutput(result)).toContain('CPA URL contains unsupported characters');
  });

  it('rejects CPA URLs with query strings', () => {
    const result = runInstaller({
      CPAMP_INSTALL_MODE: 'cpamp',
      CPAMP_DEPLOY_METHOD: 'docker',
      CPAMP_CPA_CONNECTION_MODE: 'env',
      CPAMP_CPA_URL: 'http://host.docker.internal:8317?x=y',
      CPAMP_CPA_MANAGEMENT_KEY: 'cpa_existing_management_key',
    });

    expect(result.status).toBe(1);
    expect(combinedOutput(result)).toContain('CPA URL contains unsupported characters');
  });

  it('rejects Docker image references with compose interpolation syntax', () => {
    const result = runInstaller({
      CPAMP_INSTALL_MODE: 'stack',
      CPAMP_DEPLOY_METHOD: 'docker',
      CPAMP_IMAGE: 'seakee/cpa-manager-plus:${BAD}',
    });

    expect(result.status).toBe(1);
    expect(combinedOutput(result)).toContain('CPAMP Docker image contains unsupported characters');
  });

  it('rejects Docker Compose project names that Docker would normalize differently', () => {
    const result = runInstaller({
      CPAMP_PROJECT_NAME: 'CPAMP.Bad',
      CPAMP_INSTALL_MODE: 'stack',
      CPAMP_DEPLOY_METHOD: 'docker',
    });

    expect(result.status).toBe(1);
    expect(combinedOutput(result)).toContain(
      'Docker Compose project name contains unsupported characters'
    );
  });

  it('rejects an empty persisted Docker Compose project name', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(path.join(installDir, '.env'), 'COMPOSE_PROJECT_NAME=\nCPAMP_PORT=18317\n');
      writeFileSync(
        path.join(installDir, 'compose.yaml'),
        'services:\n  cpa-manager-plus:\n    image: example/cpamp:v1\n'
      );
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_DRY_RUN: '1',
          CPAMP_OPERATION: 'upgrade',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('Docker Compose project name must not be empty');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
    }
  });

  it('fails instead of looping when the random source yields no alphanumeric characters', () => {
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));

    try {
      const fakeOpenSSL = path.join(fakeBin, 'openssl');
      writeFileSync(fakeOpenSSL, '#!/usr/bin/env bash\nprintf -- "----"\n');
      chmodSync(fakeOpenSSL, 0o755);

      const result = runInstaller({
        CPAMP_INSTALL_MODE: 'stack',
        CPAMP_DEPLOY_METHOD: 'docker',
        PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain(
        'Random source produced no usable alphanumeric characters'
      );
    } finally {
      rmSync(fakeBin, { recursive: true, force: true });
    }
  });

  it('generates full Docker config with CPA image paths', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));

    try {
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_SKIP_EXECUTE: '1',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_MODE: 'stack',
          CPAMP_DEPLOY_METHOD: 'docker',
          CPAMP_INSTALL_DIR: installDir,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);

      const compose = readFileSync(path.join(installDir, 'compose.yaml'), 'utf8');
      const cpaConfig = readFileSync(path.join(installDir, 'cliproxyapi/config.yaml'), 'utf8');
      const adminKey = readFileSync(
        path.join(installDir, 'secrets/cpamp-admin-key'),
        'utf8'
      ).trim();
      const cpaManagementKey = readFileSync(
        path.join(installDir, 'secrets/cpa-management-key'),
        'utf8'
      ).trim();
      const demoClientKey = readFileSync(
        path.join(installDir, 'secrets/cpa-demo-client-key'),
        'utf8'
      ).trim();

      expect(compose).toContain('./cliproxyapi/config.yaml:/CLIProxyAPI/config.yaml');
      expect(compose).toContain('./cliproxyapi/auths:/root/.cli-proxy-api');
      expect(compose).toContain('./cliproxyapi/logs:/CLIProxyAPI/logs');
      expect(compose).not.toContain('CPA_UPSTREAM_URL');
      expect(compose).not.toContain('CPA_MANAGEMENT_KEY_FILE');
      expect(compose).not.toContain('cpa_management_key');
      expect(result.stdout).toContain('store-cpa-connection');
      expect(adminKey).toMatch(/^cpamp_[A-Za-z0-9]{32}$/);
      expect(cpaManagementKey).toMatch(/^cpa_[A-Za-z0-9]{32}$/);
      expect(demoClientKey).toMatch(/^sk-[A-Za-z0-9]{64}$/);
      expect(cpaConfig).toContain('auth-dir: "/root/.cli-proxy-api"');
      expect(cpaConfig).toContain(`secret-key: "${cpaManagementKey}"`);
      expect(cpaConfig).toContain(`api-keys:\n  - "${demoClientKey}"`);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
    }
  });

  it('generates CPAMP-only Docker config for a host CPA URL', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));

    try {
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_SKIP_EXECUTE: '1',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_MODE: 'cpamp',
          CPAMP_DEPLOY_METHOD: 'docker',
          CPAMP_CPA_CONNECTION_MODE: 'env',
          CPAMP_CPA_URL: 'http://host.docker.internal:8317',
          CPAMP_CPA_MANAGEMENT_KEY: 'cpa_existing_management_key',
          CPAMP_INSTALL_DIR: installDir,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);

      const envFile = readFileSync(path.join(installDir, '.env'), 'utf8');
      const compose = readFileSync(path.join(installDir, 'compose.yaml'), 'utf8');

      expect(envFile).not.toContain('CPA_UPSTREAM_URL');
      expect(compose).not.toContain('CPA_UPSTREAM_URL');
      expect(compose).not.toContain('CPA_MANAGEMENT_KEY_FILE');
      expect(compose).not.toContain('cpa_management_key');
      expect(result.stdout).toContain('store-cpa-connection');
      expect(result.stdout).toContain('rm -f');
      expect(result.stdout.indexOf('store-cpa-connection')).toBeLessThan(
        result.stdout.indexOf('docker compose up -d')
      );
      expect(result.stdout.indexOf('docker compose up -d')).toBeLessThan(
        result.stdout.indexOf('/health')
      );
      expect(result.stdout.indexOf('/health')).toBeLessThan(result.stdout.indexOf('/status'));
      expect(result.stdout.indexOf('/status')).toBeLessThan(result.stdout.indexOf('rm -f'));
      expect(result.stdout).toContain('Deployment config generated');
      if (process.platform === 'linux') {
        expect(compose).toContain('host.docker.internal:host-gateway');
      }
    } finally {
      rmSync(installDir, { recursive: true, force: true });
    }
  });

  it('reuses an existing CPA Management Key secret for CPAMP-only Docker env installs', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(
        path.join(installDir, 'secrets/cpa-management-key'),
        'cpa_reused_management_key\n'
      );

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_SKIP_EXECUTE: '1',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_MODE: 'cpamp',
          CPAMP_DEPLOY_METHOD: 'docker',
          CPAMP_CPA_CONNECTION_MODE: 'env',
          CPAMP_CPA_URL: 'http://host.docker.internal:8317',
          CPAMP_INSTALL_DIR: installDir,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      expect(readFileSync(path.join(installDir, 'secrets/cpa-management-key'), 'utf8').trim()).toBe(
        'cpa_reused_management_key'
      );
      const compose = readFileSync(path.join(installDir, 'compose.yaml'), 'utf8');
      expect(compose).not.toContain('CPA_MANAGEMENT_KEY_FILE');
      expect(compose).not.toContain('cpa_management_key');
      expect(result.stdout).toContain('store-cpa-connection');
      expect(result.stdout).toContain('Temporary CPA Management Key file');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
    }
  });

  it('reuses an existing CPA Management Key secret during dry runs', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(
        path.join(installDir, 'secrets/cpa-management-key'),
        'cpa_reused_management_key\n'
      );

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_DRY_RUN: '1',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_MODE: 'cpamp',
          CPAMP_DEPLOY_METHOD: 'docker',
          CPAMP_CPA_CONNECTION_MODE: 'env',
          CPAMP_CPA_URL: 'http://host.docker.internal:8317',
          CPAMP_INSTALL_DIR: installDir,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      expect(combinedOutput(result)).not.toContain('secrets/cpa-management-key must not be empty');
      expect(result.stdout).toContain('first setup');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
    }
  });

  it('imports a fresh Docker CPA connection once and removes the temporary secret', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );

    try {
      writeFakeDocker(fakeBin);
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_MODE: 'cpamp',
          CPAMP_DEPLOY_METHOD: 'docker',
          CPAMP_CPA_CONNECTION_MODE: 'env',
          CPAMP_CPA_URL: 'http://host.docker.internal:8317',
          CPAMP_CPA_MANAGEMENT_KEY: 'cpa_one_time_import_key',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_LOG: dockerLog,
          FAKE_DOCKER_AUTH_OK: '1',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      const calls = readFileSync(dockerLog, 'utf8');
      const compose = readFileSync(path.join(installDir, 'compose.yaml'), 'utf8');
      expect(calls).toContain('compose run --rm --no-deps');
      expect(calls).toContain('-e CPA_MANAGEMENT_KEY_FILE=/dev/null');
      expect(calls).toContain(':/run/cpamp-import/cpa-management-key:ro');
      expect(calls).toContain('--management-key-file /run/cpamp-import/cpa-management-key');
      expect(calls).toContain('store-cpa-connection');
      expect(calls).toContain('--db-path /data/usage.sqlite');
      expect(calls).toContain('--data-key-path /data/data.key');
      expect(compose).not.toContain('CPA_MANAGEMENT_KEY_FILE');
      expect(compose).not.toContain('CPA_UPSTREAM_URL');
      expect(existsSync(path.join(installDir, 'secrets/cpa-management-key'))).toBe(false);
      expect(result.stdout).toContain('CPA connection imported into encrypted SQLite storage');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('blocks non-interactive installs when an orphaned Docker data volume exists', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));

    try {
      writeFakeDocker(fakeBin);
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_SKIP_EXECUTE: '1',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_VOLUME_EXISTS: '1',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('old Docker data volume exists');
      expect(existsSync(path.join(installDir, 'compose.yaml'))).toBe(false);
      expect(existsSync(path.join(installDir, 'secrets/cpamp-admin-key'))).toBe(false);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
    }
  });

  it('requires the original install scope for non-interactive orphan-volume repair', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));

    try {
      writeFakeDocker(fakeBin);
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'repair',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_VOLUME_EXISTS: '1',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('requires CPAMP_INSTALL_MODE=stack or cpamp');
      expect(existsSync(path.join(installDir, 'compose.yaml'))).toBe(false);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
    }
  });

  it('rejects skipped execution for orphan-volume repair before writing a new secret', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));

    try {
      writeFakeDocker(fakeBin);
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'repair',
          CPAMP_SKIP_EXECUTE: '1',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_MODE: 'stack',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_VOLUME_EXISTS: '1',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('cannot use CPAMP_SKIP_EXECUTE=1');
      expect(existsSync(path.join(installDir, 'secrets/cpamp-admin-key'))).toBe(false);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
    }
  });

  it('fails before writing Docker config when the Docker daemon is unavailable', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));

    try {
      writeFakeDocker(fakeBin);
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_MODE: 'stack',
          CPAMP_DEPLOY_METHOD: 'docker',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_DAEMON_OK: '0',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('Docker daemon is not available');
      expect(existsSync(path.join(installDir, '.env'))).toBe(false);
      expect(existsSync(path.join(installDir, 'compose.yaml'))).toBe(false);
      expect(existsSync(path.join(installDir, 'secrets/cpamp-admin-key'))).toBe(false);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
    }
  });

  it('does not create a replacement admin secret before repair preflight succeeds', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));

    try {
      writeFileSync(
        path.join(installDir, '.env'),
        'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\n'
      );
      writeFileSync(
        path.join(installDir, 'compose.yaml'),
        'services:\n  cpa-manager-plus:\n    image: ${CPAMP_IMAGE}\n'
      );
      writeFakeDocker(fakeBin);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'repair',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_DAEMON_OK: '0',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('Docker daemon is not available');
      expect(existsSync(path.join(installDir, 'secrets/cpamp-admin-key'))).toBe(false);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
    }
  });

  it('does not create a half-repaired admin secret when repair execution is skipped', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));

    try {
      writeFileSync(
        path.join(installDir, '.env'),
        'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\n'
      );
      writeFileSync(
        path.join(installDir, 'compose.yaml'),
        'services:\n  cpa-manager-plus:\n    image: ${CPAMP_IMAGE}\n'
      );

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'repair',
          CPAMP_SKIP_EXECUTE: '1',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      expect(existsSync(path.join(installDir, 'secrets/cpamp-admin-key'))).toBe(false);
      expect(result.stdout).toContain('upgrade or repair commands were skipped');
      expect(result.stdout).not.toContain('Admin key saved');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
    }
  });

  it('repairs an orphaned Docker deployment and verifies the generated admin key', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );

    try {
      writeFakeDocker(fakeBin);
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'repair',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_MODE: 'stack',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_LOG: dockerLog,
          FAKE_DOCKER_VOLUME_EXISTS: '1',
          FAKE_DOCKER_AUTH_OK: '1',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      expect(readFileSync(dockerLog, 'utf8')).toContain(
        'compose run --rm cpa-manager-plus reset-admin-key --admin-key-file /run/secrets/cpamp_admin_key'
      );
      expect(readFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'utf8').trim()).toMatch(
        /^cpamp_[A-Za-z0-9]{32}$/
      );
      expect(result.stdout).toContain('Admin key verification passed');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('upgrades a managed Docker install without rewriting config or secrets', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const envContent =
      'COMPOSE_PROJECT_NAME=oldproject\nCOMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=19999\nCPAMP_PORT=18317\n';
    const composeContent = `services:
  cpa-manager-plus:
    image: \${CPAMP_IMAGE}
  unrelated-sidecar:
    image: example/sidecar:v1
    environment:
      CPA_UPSTREAM_URL: "\${CPA_UPSTREAM_URL}"
      CPA_MANAGEMENT_KEY: "\${CPA_MANAGEMENT_KEY}"
`;
    const secretContent = 'cpamp_existing_admin_key\n';

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(path.join(installDir, '.env'), envContent);
      writeFileSync(path.join(installDir, 'compose.yaml'), composeContent);
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), secretContent);
      writeFakeDocker(fakeBin);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'upgrade',
          COMPOSE_PROJECT_NAME: 'wrong-project',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_LOG: dockerLog,
          FAKE_DOCKER_AUTH_OK: '1',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      expect(readFileSync(path.join(installDir, '.env'), 'utf8')).toBe(envContent);
      expect(readFileSync(path.join(installDir, 'compose.yaml'), 'utf8')).toBe(composeContent);
      expect(readFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'utf8')).toBe(
        secretContent
      );
      expect(readFileSync(dockerLog, 'utf8')).toContain('compose pull');
      expect(readFileSync(dockerLog, 'utf8')).toContain('compose up -d');
      expect(readFileSync(dockerLog, 'utf8')).not.toContain('reset-admin-key');
      expect(readFileSync(dockerLog, 'utf8')).toContain('cpamp|compose pull');
      expect(result.stdout).toContain('Public CPAMP port: 18317');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('migrates legacy Docker CPA env secrets with targeted config edits', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const envContent =
      'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\nCPA_UPSTREAM_URL=http://host.docker.internal:8317\nCUSTOM_VALUE=keep-me\n';
    const composeContent = `services:
  cpa-manager-plus:
    image: \${CPAMP_IMAGE}
    environment:
      CPA_UPSTREAM_URL: "\${CPA_UPSTREAM_URL}"
      CPA_MANAGEMENT_KEY_FILE: "/run/secrets/cpa_management_key"
      CUSTOM_VALUE: "keep-me"
    volumes:
      - cpa-manager-plus-data:/data
    secrets:
      - cpamp_admin_key
      - cpa_management_key
secrets:
  cpamp_admin_key:
    file: ./secrets/cpamp-admin-key
  cpa_management_key:
    file: ./secrets/cpa-management-key
`;

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(path.join(installDir, '.env'), envContent);
      writeFileSync(path.join(installDir, 'compose.yaml'), composeContent);
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      writeFileSync(path.join(installDir, 'secrets/cpa-management-key'), 'cpa_legacy_key\n');
      writeFakeDocker(fakeBin);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'upgrade',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_LOG: dockerLog,
          FAKE_DOCKER_AUTH_OK: '1',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      const migratedEnv = readFileSync(path.join(installDir, '.env'), 'utf8');
      const migratedCompose = readFileSync(path.join(installDir, 'compose.yaml'), 'utf8');
      const calls = readFileSync(dockerLog, 'utf8');
      expect(migratedEnv).not.toContain('CPA_UPSTREAM_URL');
      expect(migratedEnv).toContain('CUSTOM_VALUE=keep-me');
      expect(migratedCompose).not.toContain('CPA_UPSTREAM_URL');
      expect(migratedCompose).not.toContain('CPA_MANAGEMENT_KEY_FILE');
      expect(migratedCompose).not.toContain('cpa_management_key');
      expect(migratedCompose).toContain('CUSTOM_VALUE: "keep-me"');
      expect(existsSync(path.join(installDir, 'secrets/cpa-management-key'))).toBe(false);
      expect(
        readdirSync(installDir).some((name) =>
          name.startsWith('compose.yaml.cpa-key-migration.bak.')
        )
      ).toBe(true);
      expect(calls).toContain('compose stop cpa-manager-plus');
      expect(calls).toContain('store-cpa-connection');
      expect(calls).toContain('compose up -d');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('migrates legacy Docker Compose secrets written in long syntax', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const envContent =
      'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\nCPA_UPSTREAM_URL=http://host.docker.internal:8317\n';
    const composeContent = `services:
  cpa-manager-plus:
    image: \${CPAMP_IMAGE}
    environment:
      CPA_UPSTREAM_URL: "\${CPA_UPSTREAM_URL}"
      CPA_MANAGEMENT_KEY_FILE: "/run/secrets/cpa_management_key"
      CUSTOM_VALUE: "keep-me"
    secrets:
      - source: cpamp_admin_key
        target: cpamp_admin_key
      - source: cpa_management_key
        target: cpa_management_key
secrets:
  cpamp_admin_key:
    file: ./secrets/cpamp-admin-key
  cpa_management_key:
    file: ./secrets/cpa-management-key
`;

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(path.join(installDir, '.env'), envContent);
      writeFileSync(path.join(installDir, 'compose.yaml'), composeContent);
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      writeFileSync(path.join(installDir, 'secrets/cpa-management-key'), 'cpa_legacy_key\n');
      writeFakeDocker(fakeBin);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'upgrade',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_LOG: dockerLog,
          FAKE_DOCKER_AUTH_OK: '1',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      const migratedEnv = readFileSync(path.join(installDir, '.env'), 'utf8');
      const migratedCompose = readFileSync(path.join(installDir, 'compose.yaml'), 'utf8');
      expect(migratedEnv).not.toContain('CPA_UPSTREAM_URL');
      expect(migratedCompose).not.toContain('CPA_UPSTREAM_URL');
      expect(migratedCompose).not.toContain('CPA_MANAGEMENT_KEY_FILE');
      expect(migratedCompose).not.toContain('cpa_management_key');
      expect(migratedCompose).toContain('source: cpamp_admin_key');
      expect(migratedCompose).toContain('target: cpamp_admin_key');
      expect(existsSync(path.join(installDir, 'secrets/cpa-management-key'))).toBe(false);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('migrates a legacy Docker direct CPA Management Key environment variable', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const envContent =
      'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\nCPA_UPSTREAM_URL=http://host.docker.internal:8317\nCPA_MANAGEMENT_KEY=cpa_direct_key\nCPA_MANAGEMENT_KEY_FILE=/run/secrets/cpa_management_key\n';
    const composeContent = `services:
  cpa-manager-plus:
    image: \${CPAMP_IMAGE}
    environment:
      - CPA_UPSTREAM_URL=\${CPA_UPSTREAM_URL}
      - CPA_MANAGEMENT_KEY=\${CPA_MANAGEMENT_KEY}
      - CUSTOM_VALUE=keep-me
    secrets:
      - cpamp_admin_key
secrets:
  cpamp_admin_key:
    file: ./secrets/cpamp-admin-key
`;

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(path.join(installDir, '.env'), envContent);
      writeFileSync(path.join(installDir, 'compose.yaml'), composeContent);
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      writeFakeDocker(fakeBin);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'upgrade',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_LOG: dockerLog,
          FAKE_DOCKER_AUTH_OK: '1',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      const migratedEnv = readFileSync(path.join(installDir, '.env'), 'utf8');
      const migratedCompose = readFileSync(path.join(installDir, 'compose.yaml'), 'utf8');
      const calls = readFileSync(dockerLog, 'utf8');
      expect(migratedEnv).not.toContain('CPA_UPSTREAM_URL');
      expect(migratedEnv).not.toContain('CPA_MANAGEMENT_KEY');
      expect(migratedCompose).not.toContain('CPA_UPSTREAM_URL');
      expect(migratedCompose).not.toContain('CPA_MANAGEMENT_KEY');
      expect(migratedCompose).toContain('CUSTOM_VALUE=keep-me');
      expect(calls).toContain('store-cpa-connection');
      expect(existsSync(path.join(installDir, 'secrets/cpa-management-key'))).toBe(false);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('unwraps quoted legacy Docker env values before importing the CPA connection', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const envContent =
      'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\n' +
      'CPA_UPSTREAM_URL="http://host.docker.internal:8317"\n' +
      "export CPA_MANAGEMENT_KEY='cpa_quoted_key'\n";
    const composeContent = `services:
  cpa-manager-plus:
    image: \${CPAMP_IMAGE}
    environment:
      - CPA_UPSTREAM_URL=\${CPA_UPSTREAM_URL}
      - CPA_MANAGEMENT_KEY=\${CPA_MANAGEMENT_KEY}
    secrets:
      - cpamp_admin_key
secrets:
  cpamp_admin_key:
    file: ./secrets/cpamp-admin-key
`;

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(path.join(installDir, '.env'), envContent);
      writeFileSync(path.join(installDir, 'compose.yaml'), composeContent);
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      writeFakeDocker(fakeBin);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'upgrade',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_LOG: dockerLog,
          FAKE_DOCKER_IMPORT_OK: '0',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(readFileSync(path.join(installDir, 'secrets/cpa-management-key'), 'utf8')).toBe(
        'cpa_quoted_key\n'
      );
      expect(combinedOutput(result)).toContain(
        'previous runtime config and temporary secret were preserved'
      );
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('imports a legacy Docker CPA key from CPA_MANAGEMENT_KEY_FILE without deleting an external file', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const externalKeyPath = path.join(installDir, 'external-cpa-management-key');
    const envContent =
      'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\n' +
      'CPA_UPSTREAM_URL=http://host.docker.internal:8317\n' +
      'CPA_MANAGEMENT_KEY_FILE=./external-cpa-management-key\n';
    const composeContent = `services:
  cpa-manager-plus:
    image: \${CPAMP_IMAGE}
    environment:
      CPA_UPSTREAM_URL: "\${CPA_UPSTREAM_URL}"
      CPA_MANAGEMENT_KEY_FILE: "\${CPA_MANAGEMENT_KEY_FILE}"
    `;

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(path.join(installDir, '.env'), envContent);
      writeFileSync(path.join(installDir, 'compose.yaml'), composeContent);
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      writeFileSync(externalKeyPath, 'cpa_external_file_key\n');
      chmodSync(externalKeyPath, 0o640);
      writeFakeDocker(fakeBin);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'upgrade',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_LOG: dockerLog,
          FAKE_DOCKER_AUTH_OK: '1',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      expect(readFileSync(externalKeyPath, 'utf8')).toBe('cpa_external_file_key\n');
      expect(statSync(externalKeyPath).mode & 0o777).toBe(0o640);
      const calls = readFileSync(dockerLog, 'utf8');
      expect(calls).toContain(
        `-v ${realpathSync(externalKeyPath)}:/run/cpamp-import/cpa-management-key:ro`
      );
      expect(readFileSync(path.join(installDir, 'compose.yaml'), 'utf8')).not.toContain(
        'CPA_MANAGEMENT_KEY_FILE'
      );
      expect(result.stdout).not.toContain(`rm -f "${externalKeyPath}"`);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('uses process CPA inputs instead of stale env and installer-managed secret values', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const externalDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-external-secret-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(os.tmpdir(), `cpamp-installer-docker-${process.pid}-${Date.now()}.log`);
    const importedKeyLog = path.join(os.tmpdir(), `cpamp-imported-key-${process.pid}-${Date.now()}`);
    const externalKeyPath = path.join(externalDir, 'process-cpa-key');
    const managedKeyPath = path.join(installDir, 'secrets/cpa-management-key');
    const composeContent = `services:
  cpa-manager-plus:
    image: \${CPAMP_IMAGE}
    environment:
      CPA_UPSTREAM_URL: "\${CPA_UPSTREAM_URL}"
      CPA_MANAGEMENT_KEY_FILE: "\${CPA_MANAGEMENT_KEY_FILE}"
    secrets:
      - cpa_management_key
secrets:
  cpa_management_key:
    file: ./secrets/cpa-management-key
`;

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(
        path.join(installDir, '.env'),
        'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\n' +
          'CPA_UPSTREAM_URL=http://stale.example:8317\n' +
          'CPA_MANAGEMENT_KEY_FILE=./secrets/cpa-management-key\n'
      );
      writeFileSync(path.join(installDir, 'compose.yaml'), composeContent);
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      writeFileSync(managedKeyPath, 'cpa_stale_managed_key\n');
      writeFileSync(externalKeyPath, 'cpa_process_file_key\n');
      chmodSync(externalKeyPath, 0o640);
      writeFakeDocker(fakeBin);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPA_UPSTREAM_URL: 'http://process.example:8317',
          CPA_MANAGEMENT_KEY: '',
          CPA_MANAGEMENT_KEY_FILE: externalKeyPath,
          CPAMP_OPERATION: 'upgrade',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_LOG: dockerLog,
          FAKE_DOCKER_IMPORTED_KEY_LOG: importedKeyLog,
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      expect(readFileSync(importedKeyLog, 'utf8')).toBe('cpa_process_file_key\n');
      expect(readFileSync(dockerLog, 'utf8')).toContain('--cpa-base-url http://process.example:8317');
      expect(readFileSync(externalKeyPath, 'utf8')).toBe('cpa_process_file_key\n');
      expect(statSync(externalKeyPath).mode & 0o777).toBe(0o640);
      expect(readFileSync(managedKeyPath, 'utf8')).toBe('cpa_stale_managed_key\n');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(externalDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
      rmSync(importedKeyLog, { force: true });
    }
  });

  it('materializes a process CPA key separately from a stale managed secret file', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(os.tmpdir(), `cpamp-installer-docker-${process.pid}-${Date.now()}.log`);
    const importedKeyLog = path.join(os.tmpdir(), `cpamp-imported-key-${process.pid}-${Date.now()}`);
    const managedKeyPath = path.join(installDir, 'secrets/cpa-management-key');
    const composeContent = `services:
  cpa-manager-plus:
    image: \${CPAMP_IMAGE}
    environment:
      CPA_UPSTREAM_URL: "\${CPA_UPSTREAM_URL}"
      CPA_MANAGEMENT_KEY: "\${CPA_MANAGEMENT_KEY}"
`;

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(
        path.join(installDir, '.env'),
        'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\n' +
          'CPA_UPSTREAM_URL=http://stale.example:8317\nCPA_MANAGEMENT_KEY=cpa_stale_env_key\n'
      );
      writeFileSync(path.join(installDir, 'compose.yaml'), composeContent);
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      writeFileSync(managedKeyPath, 'cpa_stale_managed_key\n');
      writeFakeDocker(fakeBin);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPA_UPSTREAM_URL: 'http://process.example:8317',
          CPA_MANAGEMENT_KEY: 'cpa_process_direct_key',
          CPA_MANAGEMENT_KEY_FILE: '',
          CPAMP_OPERATION: 'upgrade',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_LOG: dockerLog,
          FAKE_DOCKER_IMPORTED_KEY_LOG: importedKeyLog,
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      expect(readFileSync(importedKeyLog, 'utf8')).toBe('cpa_process_direct_key\n');
      expect(readFileSync(managedKeyPath, 'utf8')).toBe('cpa_stale_managed_key\n');
      expect(
        readdirSync(path.join(installDir, 'secrets')).some((name) =>
          name.startsWith('cpa-management-key.import.')
        )
      ).toBe(false);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
      rmSync(importedKeyLog, { force: true });
    }
  });

  it('ignores stale CPA declarations and secret files that Compose does not reference', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(os.tmpdir(), `cpamp-installer-docker-${process.pid}-${Date.now()}.log`);
    const envContent =
      'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\n' +
      'CPA_UPSTREAM_URL=http://stale.example:8317\nCPA_MANAGEMENT_KEY=cpa_stale_key\n';
    const composeContent = 'services:\n  cpa-manager-plus:\n    image: ${CPAMP_IMAGE}\n';

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(path.join(installDir, '.env'), envContent);
      writeFileSync(path.join(installDir, 'compose.yaml'), composeContent);
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      writeFileSync(path.join(installDir, 'secrets/cpa-management-key'), 'cpa_stale_file_key\n');
      writeFakeDocker(fakeBin);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPA_UPSTREAM_URL: '',
          CPA_MANAGEMENT_KEY: '',
          CPA_MANAGEMENT_KEY_FILE: '',
          CPAMP_OPERATION: 'upgrade',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_LOG: dockerLog,
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      expect(readFileSync(path.join(installDir, '.env'), 'utf8')).toBe(envContent);
      expect(readFileSync(path.join(installDir, 'compose.yaml'), 'utf8')).toBe(composeContent);
      // The unreferenced managed key file is finalized: the stored connection
      // is re-verified through the CPA proxy and the plaintext key is removed.
      expect(readFileSync(dockerLog, 'utf8')).not.toContain('store-cpa-connection');
      expect(readFileSync(dockerLog, 'utf8')).not.toContain('manager-data-snapshot');
      expect(readFileSync(dockerLog, 'utf8')).toContain('/v0/management/config');
      expect(existsSync(path.join(installDir, 'secrets/cpa-management-key'))).toBe(false);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('rejects an unterminated quoted legacy Docker CPA key', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const envPath = path.join(installDir, '.env');
    const composePath = path.join(installDir, 'compose.yaml');

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(
        envPath,
        'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\n' +
          'CPA_UPSTREAM_URL=http://host.docker.internal:8317\n' +
          "CPA_MANAGEMENT_KEY='unterminated\n"
      );
      writeFileSync(
        composePath,
        `services:
  cpa-manager-plus:
    image: \${CPAMP_IMAGE}
    environment:
      - CPA_UPSTREAM_URL=\${CPA_UPSTREAM_URL}
      - CPA_MANAGEMENT_KEY=\${CPA_MANAGEMENT_KEY}
`
      );
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      writeFakeDocker(fakeBin);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'upgrade',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('CPA Management Key is referenced');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
    }
  });

  it('fails closed when a sidecar still references the legacy CPA env or secret', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const envContent =
      'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\nCPA_UPSTREAM_URL=http://host.docker.internal:8317\n';
    const composeContent = `services:
  cpa-manager-plus:
    image: \${CPAMP_IMAGE}
    environment:
      CPA_UPSTREAM_URL: "\${CPA_UPSTREAM_URL}"
      CPA_MANAGEMENT_KEY_FILE: "/run/secrets/cpa_management_key"
    secrets:
      - cpamp_admin_key
      - cpa_management_key
  sidecar:
    image: example/sidecar:v1
    environment:
      CPA_MANAGEMENT_KEY_FILE: "/run/secrets/cpa_management_key"
    secrets:
      - cpa_management_key
secrets:
  cpamp_admin_key:
    file: ./secrets/cpamp-admin-key
  cpa_management_key:
    file: ./secrets/cpa-management-key
`;

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(path.join(installDir, '.env'), envContent);
      writeFileSync(path.join(installDir, 'compose.yaml'), composeContent);
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      writeFileSync(path.join(installDir, 'secrets/cpa-management-key'), 'cpa_legacy_key\n');
      writeFakeDocker(fakeBin);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'upgrade',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_LOG: dockerLog,
          FAKE_DOCKER_AUTH_OK: '1',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('previous runtime config and temporary secret were preserved');
      expect(readFileSync(path.join(installDir, '.env'), 'utf8')).toBe(envContent);
      expect(readFileSync(path.join(installDir, 'compose.yaml'), 'utf8')).toBe(composeContent);
      expect(readFileSync(path.join(installDir, 'secrets/cpa-management-key'), 'utf8')).toBe(
        'cpa_legacy_key\n'
      );
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('keeps a legacy Docker migration untouched when execution is skipped', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const envContent =
      'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\nCPA_UPSTREAM_URL=http://host.docker.internal:8317\n';
    const composeContent = `services:
  cpa-manager-plus:
    image: \${CPAMP_IMAGE}
    environment:
      CPA_UPSTREAM_URL: "\${CPA_UPSTREAM_URL}"
      CPA_MANAGEMENT_KEY_FILE: "/run/secrets/cpa_management_key"
    secrets:
      - cpamp_admin_key
      - cpa_management_key
secrets:
  cpamp_admin_key:
    file: ./secrets/cpamp-admin-key
  cpa_management_key:
    file: ./secrets/cpa-management-key
`;

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(path.join(installDir, '.env'), envContent);
      writeFileSync(path.join(installDir, 'compose.yaml'), composeContent);
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      writeFileSync(path.join(installDir, 'secrets/cpa-management-key'), 'cpa_legacy_key\n');
      writeFakeDocker(fakeBin);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'upgrade',
          CPAMP_SKIP_EXECUTE: '1',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      expect(readFileSync(path.join(installDir, '.env'), 'utf8')).toBe(envContent);
      expect(readFileSync(path.join(installDir, 'compose.yaml'), 'utf8')).toBe(composeContent);
      expect(existsSync(path.join(installDir, 'secrets/cpa-management-key'))).toBe(true);
      expect(result.stdout).toContain('CPAMP_OPERATION=upgrade');
      expect(result.stdout).toContain('CPAMP_SKIP_EXECUTE=0');
      expect(result.stdout).not.toContain('rm -f');
      expect(result.stdout).not.toContain('docker compose stop cpa-manager-plus');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
    }
  });

  it('keeps legacy Docker config and secret when CPA connection import conflicts', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const envContent =
      'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\nCPA_UPSTREAM_URL=http://host.docker.internal:8317\n';
    const composeContent = `services:
  cpa-manager-plus:
    image: \${CPAMP_IMAGE}
    environment:
      CPA_UPSTREAM_URL: "\${CPA_UPSTREAM_URL}"
      CPA_MANAGEMENT_KEY_FILE: "/run/secrets/cpa_management_key"
    secrets:
      - cpamp_admin_key
      - cpa_management_key
secrets:
  cpamp_admin_key:
    file: ./secrets/cpamp-admin-key
  cpa_management_key:
    file: ./secrets/cpa-management-key
`;

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(path.join(installDir, '.env'), envContent);
      writeFileSync(path.join(installDir, 'compose.yaml'), composeContent);
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      writeFileSync(path.join(installDir, 'secrets/cpa-management-key'), 'cpa_legacy_key\n');
      writeFakeDocker(fakeBin);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'upgrade',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_LOG: dockerLog,
          FAKE_DOCKER_IMPORT_OK: '0',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain(
        'previous runtime config and temporary secret were preserved'
      );
      expect(readFileSync(path.join(installDir, '.env'), 'utf8')).toBe(envContent);
      expect(readFileSync(path.join(installDir, 'compose.yaml'), 'utf8')).toBe(composeContent);
      expect(readFileSync(path.join(installDir, 'secrets/cpa-management-key'), 'utf8')).toBe(
        'cpa_legacy_key\n'
      );
      expect(readFileSync(dockerLog, 'utf8')).toContain('compose up -d');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('restores legacy Docker config when post-import admin verification fails', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const dockerDataDir = path.join(installDir, 'fake-docker-data');
    const dbPath = path.join(dockerDataDir, 'usage.sqlite');
    const dataKeyPath = path.join(dockerDataDir, 'data.key');
    const snapshotStore = path.join(installDir, 'fake-docker-snapshot');
    const envContent =
      'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\nCPA_UPSTREAM_URL=http://host.docker.internal:8317\n';
    const composeContent = `services:
  cpa-manager-plus:
    image: \${CPAMP_IMAGE}
    environment:
      CPA_UPSTREAM_URL: "\${CPA_UPSTREAM_URL}"
      CPA_MANAGEMENT_KEY_FILE: "/run/secrets/cpa_management_key"
    secrets:
      - cpamp_admin_key
      - cpa_management_key
secrets:
  cpamp_admin_key:
    file: ./secrets/cpamp-admin-key
  cpa_management_key:
    file: ./secrets/cpa-management-key
`;

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      mkdirSync(dockerDataDir, { recursive: true });
      writeFileSync(path.join(installDir, '.env'), envContent);
      writeFileSync(path.join(installDir, 'compose.yaml'), composeContent);
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      writeFileSync(path.join(installDir, 'secrets/cpa-management-key'), 'cpa_legacy_key\n');
      writeFileSync(dbPath, 'database-before\n');
      writeFileSync(`${dbPath}-wal`, 'wal-before\n');
      writeFileSync(`${dbPath}-shm`, 'shm-before\n');
      writeFileSync(`${dbPath}-journal`, 'journal-before\n');
      writeFileSync(dataKeyPath, 'data-key-before\n');
      writeFakeDocker(fakeBin);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'upgrade',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_LOG: dockerLog,
          FAKE_DOCKER_AUTH_OK: '0',
          FAKE_DOCKER_DB_PATH: dbPath,
          FAKE_DOCKER_DATA_KEY_PATH: dataKeyPath,
          FAKE_DOCKER_SNAPSHOT_STORE: snapshotStore,
          FAKE_DOCKER_MUTATE_ALL_DATA: '1',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(readFileSync(path.join(installDir, '.env'), 'utf8')).toBe(envContent);
      expect(readFileSync(path.join(installDir, 'compose.yaml'), 'utf8')).toBe(composeContent);
      expect(readFileSync(path.join(installDir, 'secrets/cpa-management-key'), 'utf8')).toBe(
        'cpa_legacy_key\n'
      );
      const calls = readFileSync(dockerLog, 'utf8');
      expect(calls.match(/compose up -d/g)).toHaveLength(2);
      expect(combinedOutput(result)).toContain('admin key verification failed');
      expect(readFileSync(dbPath, 'utf8')).toBe('database-before\n');
      expect(readFileSync(`${dbPath}-wal`, 'utf8')).toBe('wal-before\n');
      expect(readFileSync(`${dbPath}-shm`, 'utf8')).toBe('shm-before\n');
      expect(readFileSync(`${dbPath}-journal`, 'utf8')).toBe('journal-before\n');
      expect(readFileSync(dataKeyPath, 'utf8')).toBe('data-key-before\n');
      expect(existsSync(snapshotStore)).toBe(false);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('finalizes a leftover installer-managed CPA key on a successful rerun (Docker)', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(os.tmpdir(), `cpamp-installer-docker-${process.pid}-${Date.now()}.log`);
    const managedKeyPath = path.join(installDir, 'secrets/cpa-management-key');

    try {
      // Post-failed-install state: config no longer references the CPA key,
      // the connection is already stored in SQLite, and the installer-managed
      // plaintext key file from the failed run is still on disk.
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(
        path.join(installDir, '.env'),
        'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\n'
      );
      writeFileSync(
        path.join(installDir, 'compose.yaml'),
        'services:\n  cpa-manager-plus:\n    image: ${CPAMP_IMAGE}\n'
      );
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      writeFileSync(managedKeyPath, 'cpa_leftover_key\n');
      chmodSync(managedKeyPath, 0o600);
      writeFakeDocker(fakeBin);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'upgrade',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_LOG: dockerLog,
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      const calls = readFileSync(dockerLog, 'utf8');
      expect(calls).not.toContain('store-cpa-connection');
      expect(calls).not.toContain('manager-data-snapshot');
      expect(calls).toContain('/v0/management/config');
      expect(combinedOutput(result)).toContain('previous installer run');
      expect(combinedOutput(result)).toContain('Install steps completed');
      expect(existsSync(managedKeyPath)).toBe(false);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('keeps a leftover installer-managed CPA key when the rerun proxy validation fails (Docker)', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const managedKeyPath = path.join(installDir, 'secrets/cpa-management-key');

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(
        path.join(installDir, '.env'),
        'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\n'
      );
      writeFileSync(
        path.join(installDir, 'compose.yaml'),
        'services:\n  cpa-manager-plus:\n    image: ${CPAMP_IMAGE}\n'
      );
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      writeFileSync(managedKeyPath, 'cpa_leftover_key\n');
      writeFakeDocker(fakeBin);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'upgrade',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_CPA_OK: '0',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('CPA connection validation failed');
      expect(existsSync(managedKeyPath)).toBe(true);
      expect(readFileSync(managedKeyPath, 'utf8')).toBe('cpa_leftover_key\n');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
    }
  });

  it('finalizes a leftover installer-managed CPA key on a successful native rerun', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const release = writeFakeNativeRelease();
    const legacy = writeLegacyNativeInstall(installDir);
    const commandLog = path.join(installDir, 'native-command.log');
    const externalDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-external-secret-'));
    const externalKeyPath = path.join(externalDir, 'external-cpa-key');

    // Post-migration state: config.json no longer carries the legacy CPA
    // fields, so the rerun classifies the connection as stored.
    const legacyConfig = JSON.parse(readFileSync(legacy.configPath, 'utf8'));
    delete legacyConfig.cpaUpstreamUrl;
    delete legacyConfig.managementKeyFile;
    writeFileSync(legacy.configPath, `${JSON.stringify(legacyConfig, null, 2)}\n`);
    writeFileSync(externalKeyPath, 'external-key-content\n');
    chmodSync(externalKeyPath, 0o640);

    try {
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_DRY_RUN: '0',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_OPERATION: 'upgrade',
          CPAMP_VERSION: 'vnext',
          CPAMP_INSTALL_DIR: installDir,
          CPAMP_FAKE_NATIVE_ARCHIVE: release.archivePath,
          FAKE_NATIVE_COMMAND_LOG: commandLog,
          FAKE_NATIVE_DB_PATH: legacy.dbPath,
          PATH: `${release.fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      // No import may run during the stored-mode rerun; the fake binary only
      // creates the command log when store-cpa-connection is invoked.
      expect(existsSync(commandLog)).toBe(false);
      expect(existsSync(legacy.cpaKeyPath)).toBe(false);
      expect(readFileSync(legacy.dbPath, 'utf8')).toBe('existing-usage-data\n');
      expect(readFileSync(externalKeyPath, 'utf8')).toBe('external-key-content\n');
      expect(statSync(externalKeyPath).mode & 0o777).toBe(0o640);
    } finally {
      stopNativeFixtureProcess(installDir);
      rmSync(installDir, { recursive: true, force: true });
      rmSync(externalDir, { recursive: true, force: true });
      rmSync(release.fakeBin, { recursive: true, force: true });
      rmSync(release.fixtureDir, { recursive: true, force: true });
    }
  });

  it('keeps a leftover installer-managed CPA key when the native rerun proxy validation fails', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const release = writeFakeNativeRelease();
    const legacy = writeLegacyNativeInstall(installDir);
    const commandLog = path.join(installDir, 'native-command.log');

    const legacyConfig = JSON.parse(readFileSync(legacy.configPath, 'utf8'));
    delete legacyConfig.cpaUpstreamUrl;
    delete legacyConfig.managementKeyFile;
    writeFileSync(legacy.configPath, `${JSON.stringify(legacyConfig, null, 2)}\n`);

    try {
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_DRY_RUN: '0',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_OPERATION: 'upgrade',
          CPAMP_VERSION: 'vnext',
          CPAMP_INSTALL_DIR: installDir,
          CPAMP_FAKE_NATIVE_ARCHIVE: release.archivePath,
          FAKE_NATIVE_COMMAND_LOG: commandLog,
          FAKE_NATIVE_DB_PATH: legacy.dbPath,
          FAKE_NATIVE_CPA_OK: '0',
          PATH: `${release.fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('CPA connection validation failed');
      expect(existsSync(legacy.cpaKeyPath)).toBe(true);
      expect(readFileSync(legacy.dbPath, 'utf8')).toBe('existing-usage-data\n');
    } finally {
      stopNativeFixtureProcess(installDir);
      rmSync(installDir, { recursive: true, force: true });
      rmSync(release.fakeBin, { recursive: true, force: true });
      rmSync(release.fixtureDir, { recursive: true, force: true });
    }
  });

  it('does not roll back a committed Docker migration when snapshot cleanup fails', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(os.tmpdir(), `cpamp-installer-docker-${process.pid}-${Date.now()}.log`);
    const dockerDataDir = path.join(installDir, 'fake-docker-data');
    const dbPath = path.join(dockerDataDir, 'usage.sqlite');
    const dataKeyPath = path.join(dockerDataDir, 'data.key');
    const snapshotStore = path.join(installDir, 'fake-docker-snapshot');
    const envContent =
      'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\nCPA_UPSTREAM_URL=http://host.docker.internal:8317\n';
    const composeContent = `services:
  cpa-manager-plus:
    image: \${CPAMP_IMAGE}
    environment:
      CPA_UPSTREAM_URL: "\${CPA_UPSTREAM_URL}"
      CPA_MANAGEMENT_KEY_FILE: "/run/secrets/cpa_management_key"
    secrets:
      - cpamp_admin_key
      - cpa_management_key
secrets:
  cpamp_admin_key:
    file: ./secrets/cpamp-admin-key
  cpa_management_key:
    file: ./secrets/cpa-management-key
`;

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      mkdirSync(dockerDataDir, { recursive: true });
      writeFileSync(path.join(installDir, '.env'), envContent);
      writeFileSync(path.join(installDir, 'compose.yaml'), composeContent);
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      writeFileSync(path.join(installDir, 'secrets/cpa-management-key'), 'cpa_legacy_key\n');
      writeFileSync(dbPath, 'database-before\n');
      writeFileSync(dataKeyPath, 'data-key-before\n');
      writeFakeDocker(fakeBin);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'upgrade',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_LOG: dockerLog,
          FAKE_DOCKER_DB_PATH: dbPath,
          FAKE_DOCKER_DATA_KEY_PATH: dataKeyPath,
          FAKE_DOCKER_SNAPSHOT_STORE: snapshotStore,
          FAKE_DOCKER_SNAPSHOT_DELETE_OK: '0',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      expect(combinedOutput(result)).toContain('Install steps completed');
      expect(combinedOutput(result)).toContain('cleaning the Manager data snapshot failed');
      const calls = readFileSync(dockerLog, 'utf8');
      expect(calls).not.toContain('manager-data-snapshot restore');
      expect(calls.match(/compose stop/g)).toHaveLength(1);
      // The verified migration stays committed: runtime config migrated,
      // imported data retained, snapshot kept for manual removal.
      expect(readFileSync(path.join(installDir, 'compose.yaml'), 'utf8')).not.toContain(
        'CPA_MANAGEMENT_KEY_FILE'
      );
      expect(readFileSync(dbPath, 'utf8')).toContain('import-attempt');
      expect(existsSync(snapshotStore)).toBe(true);
      expect(existsSync(path.join(installDir, 'secrets/cpa-management-key'))).toBe(false);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('restores legacy Docker config when the imported CPA connection cannot be used', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const envContent =
      'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\nCPA_UPSTREAM_URL=http://host.docker.internal:8317\n';
    const composeContent = `services:
  cpa-manager-plus:
    image: \${CPAMP_IMAGE}
    environment:
      CPA_UPSTREAM_URL: "\${CPA_UPSTREAM_URL}"
      CPA_MANAGEMENT_KEY_FILE: "/run/secrets/cpa_management_key"
    secrets:
      - cpamp_admin_key
      - cpa_management_key
secrets:
  cpamp_admin_key:
    file: ./secrets/cpamp-admin-key
  cpa_management_key:
    file: ./secrets/cpa-management-key
`;

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(path.join(installDir, '.env'), envContent);
      writeFileSync(path.join(installDir, 'compose.yaml'), composeContent);
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      writeFileSync(path.join(installDir, 'secrets/cpa-management-key'), 'cpa_legacy_key\n');
      writeFakeDocker(fakeBin);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'upgrade',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_LOG: dockerLog,
          FAKE_DOCKER_CPA_OK: '0',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('CPA connection validation failed');
      expect(readFileSync(path.join(installDir, '.env'), 'utf8')).toBe(envContent);
      expect(readFileSync(path.join(installDir, 'compose.yaml'), 'utf8')).toBe(composeContent);
      expect(readFileSync(path.join(installDir, 'secrets/cpa-management-key'), 'utf8')).toBe(
        'cpa_legacy_key\n'
      );
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('repairs a managed Docker login without pulling unrelated service images', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(
        path.join(installDir, '.env'),
        'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\n'
      );
      writeFileSync(
        path.join(installDir, 'compose.yaml'),
        'services:\n  cpa-manager-plus:\n    image: ${CPAMP_IMAGE}\n'
      );
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      writeFakeDocker(fakeBin);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'repair',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_LOG: dockerLog,
          FAKE_DOCKER_AUTH_OK: '1',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      const calls = readFileSync(dockerLog, 'utf8');
      expect(calls).toContain(
        'compose run --rm cpa-manager-plus reset-admin-key --admin-key-file /run/secrets/cpamp_admin_key'
      );
      expect(calls).not.toContain('compose pull');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('does not report success when post-start admin key verification fails', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(
        path.join(installDir, '.env'),
        'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\n'
      );
      writeFileSync(
        path.join(installDir, 'compose.yaml'),
        'services:\n  cpa-manager-plus:\n    image: ${CPAMP_IMAGE}\n'
      );
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_wrong_admin_key\n');
      writeFakeDocker(fakeBin);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'upgrade',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
          FAKE_DOCKER_AUTH_OK: '0',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('admin key verification failed');
      expect(result.stdout).not.toContain('Install steps completed');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
    }
  });

  it('backs up generated config before regenerating a managed Docker install', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const oldEnv = 'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/old:v1\nCPAMP_PORT=18317\n';
    const oldCompose = 'services:\n  cpa-manager-plus:\n    image: old\n';

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(path.join(installDir, '.env'), oldEnv);
      writeFileSync(path.join(installDir, 'compose.yaml'), oldCompose);
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'regenerate',
          CPAMP_SKIP_EXECUTE: '1',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_MODE: 'cpamp',
          CPAMP_DEPLOY_METHOD: 'docker',
          CPAMP_CPA_CONNECTION_MODE: 'setup',
          CPAMP_INSTALL_DIR: installDir,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      const backupNames = readdirSync(path.join(installDir, 'backups'));
      expect(backupNames).toHaveLength(1);
      const backupDir = path.join(installDir, 'backups', backupNames[0]);
      expect(readFileSync(path.join(backupDir, '.env'), 'utf8')).toBe(oldEnv);
      expect(readFileSync(path.join(backupDir, 'compose.yaml'), 'utf8')).toBe(oldCompose);
      expect(readFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'utf8')).toBe(
        'cpamp_existing_admin_key\n'
      );
      expect(readFileSync(path.join(installDir, '.env'), 'utf8')).toContain(
        'CPAMP_IMAGE=example/old:v1'
      );
      expect(readFileSync(path.join(installDir, '.env'), 'utf8')).toContain('CPAMP_PORT=18317');
      expect(result.stdout).toContain('Previous config backed up to');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
    }
  });

  it('blocks a partial Docker install before writing additional generated files', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));

    try {
      writeFileSync(path.join(installDir, 'compose.yaml'), 'existing compose\n');

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_SKIP_EXECUTE: '1',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_MODE: 'cpamp',
          CPAMP_DEPLOY_METHOD: 'docker',
          CPAMP_CPA_CONNECTION_MODE: 'setup',
          CPAMP_INSTALL_DIR: installDir,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('Non-interactive mode requires CPAMP_OPERATION');
      expect(existsSync(path.join(installDir, '.env'))).toBe(false);
      expect(existsSync(path.join(installDir, 'secrets/cpamp-admin-key'))).toBe(false);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
    }
  });

  it('does not change existing admin-secret permissions during dry runs', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const secretFile = path.join(installDir, 'secrets/cpamp-admin-key');

    try {
      mkdirSync(path.dirname(secretFile), { recursive: true });
      writeFileSync(
        path.join(installDir, '.env'),
        'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\n'
      );
      writeFileSync(
        path.join(installDir, 'compose.yaml'),
        'services:\n  cpa-manager-plus:\n    image: ${CPAMP_IMAGE}\n'
      );
      writeFileSync(secretFile, 'cpamp_existing_admin_key\n');
      chmodSync(secretFile, 0o644);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_DRY_RUN: '1',
          CPAMP_OPERATION: 'upgrade',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      expect(statSync(secretFile).mode & 0o777).toBe(0o644);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
    }
  });

  it('rejects empty existing secret files before generating config', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), '');

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_SKIP_EXECUTE: '1',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_MODE: 'stack',
          CPAMP_DEPLOY_METHOD: 'docker',
          CPAMP_INSTALL_DIR: installDir,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('secrets/cpamp-admin-key must not be empty');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
    }
  });

  it('fails when existing secret file permissions cannot be restricted', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      const fakeChmod = path.join(fakeBin, 'chmod');
      writeFileSync(fakeChmod, '#!/usr/bin/env bash\nexit 1\n');
      chmodSync(fakeChmod, 0o755);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_SKIP_EXECUTE: '1',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_MODE: 'stack',
          CPAMP_DEPLOY_METHOD: 'docker',
          CPAMP_INSTALL_DIR: installDir,
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('Unable to restrict secret file permissions');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
    }
  });

  it('does not leave partial native files when the runtime directory already exists', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const platform = process.platform === 'darwin' ? 'darwin' : 'linux';
    const arch = process.arch === 'arm64' ? 'arm64' : 'amd64';
    const packageName = `cpa-manager-plus_v1.8.1_${platform}_${arch}`;

    try {
      mkdirSync(path.join(installDir, 'runtime', packageName), { recursive: true });

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_SKIP_EXECUTE: '1',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_MODE: 'cpamp',
          CPAMP_DEPLOY_METHOD: 'native',
          CPAMP_CPA_CONNECTION_MODE: 'setup',
          CPAMP_VERSION: 'v1.8.1',
          CPAMP_INSTALL_DIR: installDir,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('Directory already exists');
      expect(existsSync(path.join(installDir, 'secrets/cpamp-admin-key'))).toBe(false);
      expect(existsSync(path.join(installDir, 'run.sh'))).toBe(false);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
    }
  });

  it('upgrades a legacy native install and imports its CPA connection without replacing existing data or settings', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const release = writeFakeNativeRelease();
    const legacy = writeLegacyNativeInstall(installDir);
    const commandLog = path.join(installDir, 'native-command.log');
    const legacyConfig = JSON.parse(readFileSync(legacy.configPath, 'utf8'));
    const legacyCPAURL = legacyConfig.cpaUpstreamUrl;
    const legacyKeyFile = legacyConfig.managementKeyFile;
    delete legacyConfig.cpaUpstreamUrl;
    delete legacyConfig.managementKeyFile;
    legacyConfig.unknownNestedSetting = { enabled: true, values: [1, 2, 3] };
    writeFileSync(
      legacy.configPath,
      `${JSON.stringify({
        ...legacyConfig,
        cpaUpstreamUrl: legacyCPAURL,
        managementKeyFile: legacyKeyFile,
      })}\n`
    );

    try {
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_DRY_RUN: '0',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_OPERATION: 'upgrade',
          CPAMP_VERSION: 'vnext',
          CPAMP_INSTALL_DIR: installDir,
          CPAMP_FAKE_NATIVE_ARCHIVE: release.archivePath,
          FAKE_NATIVE_COMMAND_LOG: commandLog,
          FAKE_NATIVE_DB_PATH: legacy.dbPath,
          PATH: `${release.fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      expect(readFileSync(legacy.dbPath, 'utf8')).toBe(
        'existing-usage-data\nimport-attempt\n'
      );
      expect(readFileSync(legacy.dataKeyPath, 'utf8')).toBe('existing-data-key\n');
      expect(readFileSync(legacy.adminKeyPath, 'utf8')).toBe('cpamp_existing_admin_key\n');
      expect(existsSync(legacy.cpaKeyPath)).toBe(false);
      expect(readFileSync(legacy.configPath, 'utf8')).toContain('managementKeyFile');

      const upgradedConfigPath = path.join(
        installDir,
        'runtime',
        release.packageName,
        'config.json'
      );
      const upgradedConfig = readFileSync(upgradedConfigPath, 'utf8');
      expect(() => JSON.parse(upgradedConfig)).not.toThrow();
      expect(upgradedConfig).not.toContain('cpaUpstreamUrl');
      expect(upgradedConfig).not.toContain('managementKeyFile');
      expect(upgradedConfig).toContain('"queue": "legacy-usage"');
      expect(upgradedConfig).toContain('"batchSize": 321');
      expect(JSON.parse(upgradedConfig).unknownNestedSetting).toEqual({
        enabled: true,
        values: [1, 2, 3],
      });
      expect(readFileSync(legacy.runPath, 'utf8')).toContain(release.packageName);
      expect(readFileSync(commandLog, 'utf8')).toContain(
        `--db-path ${realpathSync(legacy.dbPath)}`
      );
      expect(combinedOutput(result)).toContain('Admin key verification passed');
    } finally {
      stopNativeFixtureProcess(installDir);
      rmSync(installDir, { recursive: true, force: true });
      rmSync(release.fakeBin, { recursive: true, force: true });
      rmSync(release.fixtureDir, { recursive: true, force: true });
    }
  });

  it('recognizes the active native runtime on a second upgrade after multiple runtime directories exist', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const release = writeFakeNativeRelease();
    const legacy = writeLegacyNativeInstall(installDir);
    const commandLog = path.join(installDir, 'native-command.log');
    const baseEnv = {
      ...process.env,
      CPAMP_DRY_RUN: '0',
      CPAMP_NON_INTERACTIVE: '1',
      CPAMP_CONFIRM: '1',
      CPAMP_LANG: 'en-US',
      CPAMP_OPERATION: 'upgrade',
      CPAMP_VERSION: 'vnext',
      CPAMP_INSTALL_DIR: installDir,
      CPAMP_FAKE_NATIVE_ARCHIVE: release.archivePath,
      FAKE_NATIVE_COMMAND_LOG: commandLog,
      FAKE_NATIVE_DB_PATH: legacy.dbPath,
      PATH: `${release.fakeBin}${path.delimiter}${process.env.PATH || ''}`,
    };

    try {
      const first = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: baseEnv,
        encoding: 'utf8',
      });
      expect(first.status).toBe(0);
      expect(readFileSync(legacy.runPath, 'utf8')).toContain(
        `# CPAMP_RUNTIME_PACKAGE=${release.packageName}`
      );

      const second = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: baseEnv,
        encoding: 'utf8',
      });
      expect(second.status).toBe(0);
      const runtimeEntries = readdirSync(path.join(installDir, 'runtime')).filter((entry) =>
        entry.startsWith(release.packageName)
      );
      expect(runtimeEntries.length).toBe(2);
      expect(readFileSync(legacy.runPath, 'utf8')).toContain('# CPAMP_RUNTIME_PACKAGE=');
      expect(readFileSync(legacy.runPath, 'utf8')).not.toContain('# CPAMP_RUNTIME_CONFIG=');
      expect(readFileSync(legacy.dbPath, 'utf8')).toBe(
        'existing-usage-data\nimport-attempt\n'
      );
    } finally {
      stopNativeFixtureProcess(installDir);
      rmSync(installDir, { recursive: true, force: true });
      rmSync(release.fakeBin, { recursive: true, force: true });
      rmSync(release.fixtureDir, { recursive: true, force: true });
    }
  });

  it('rejects native versions that could escape the runtime directory', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));

    try {
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_SKIP_EXECUTE: '1',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_MODE: 'cpamp',
          CPAMP_DEPLOY_METHOD: 'native',
          CPAMP_CPA_CONNECTION_MODE: 'setup',
          CPAMP_VERSION: 'v1.2/evil',
          CPAMP_INSTALL_DIR: installDir,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('CPAMP version contains unsupported characters');
      expect(existsSync(path.join(installDir, 'runtime'))).toBe(false);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
    }
  });

  it('rejects a native runtime marker that contains path separators', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const legacy = writeLegacyNativeInstall(installDir);
    writeFileSync(
      legacy.runPath,
      `#!/usr/bin/env bash\n# CPAMP_RUNTIME_PACKAGE=../../outside\ncd "${legacy.binaryDir}"\nexec ./cpa-manager-plus\n`
    );
    const beforeRun = readFileSync(legacy.runPath, 'utf8');

    try {
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_SKIP_EXECUTE: '1',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_OPERATION: 'upgrade',
          CPAMP_VERSION: 'vnext',
          CPAMP_INSTALL_DIR: installDir,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('Native runtime package is not recognized');
      expect(readFileSync(legacy.runPath, 'utf8')).toBe(beforeRun);
      expect(readFileSync(legacy.dbPath, 'utf8')).toBe('existing-usage-data\n');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
    }
  });

  it('keeps a legacy native install untouched when upgrade execution is skipped', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const legacy = writeLegacyNativeInstall(installDir);
    const beforeRun = readFileSync(legacy.runPath, 'utf8');
    const beforeConfig = readFileSync(legacy.configPath, 'utf8');
    const beforeDB = readFileSync(legacy.dbPath, 'utf8');

    try {
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_DRY_RUN: '0',
          CPAMP_SKIP_EXECUTE: '1',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_OPERATION: 'upgrade',
          CPAMP_VERSION: 'vnext',
          CPAMP_INSTALL_DIR: installDir,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      expect(readFileSync(legacy.runPath, 'utf8')).toBe(beforeRun);
      expect(readFileSync(legacy.configPath, 'utf8')).toBe(beforeConfig);
      expect(readFileSync(legacy.dbPath, 'utf8')).toBe(beforeDB);
      expect(existsSync(legacy.cpaKeyPath)).toBe(true);
      expect(
        existsSync(
          path.join(
            installDir,
            'runtime',
            `cpa-manager-plus_vnext_${nativePlatform}_${nativeArch}`
          )
        )
      ).toBe(false);
      expect(result.stdout).toContain('store-cpa-connection');
      expect(result.stdout).toContain('CPAMP_OPERATION=upgrade');
      expect(result.stdout).toContain('CPAMP_SKIP_EXECUTE=0');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
    }
  });

  it('rejects unsupported legacy native path values before changing the install', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const legacy = writeLegacyNativeInstall(installDir);
    const beforeRun = readFileSync(legacy.runPath, 'utf8');
    const config = JSON.parse(readFileSync(legacy.configPath, 'utf8'));
    config.dataKeyPath = 123;
    writeFileSync(legacy.configPath, `${JSON.stringify(config, null, 2)}\n`);

    try {
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_SKIP_EXECUTE: '1',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_OPERATION: 'upgrade',
          CPAMP_VERSION: 'vnext',
          CPAMP_INSTALL_DIR: installDir,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain(
        'dataKeyPath as a supported string value'
      );
      expect(readFileSync(legacy.runPath, 'utf8')).toBe(beforeRun);
      expect(readFileSync(legacy.dbPath, 'utf8')).toBe('existing-usage-data\n');
      expect(existsSync(legacy.cpaKeyPath)).toBe(true);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
    }
  });

  it('refuses to stop a live process from an unrelated native PID file', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const release = writeFakeNativeRelease();
    const legacy = writeLegacyNativeInstall(installDir);
    const unrelated = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], {
      stdio: 'ignore',
    });
    const unrelatedPID = unrelated.pid;
    const beforeRun = readFileSync(legacy.runPath, 'utf8');

    try {
      expect(Number.isInteger(unrelatedPID)).toBe(true);
      writeFileSync(path.join(installDir, 'cpa-manager-plus.pid'), `${unrelatedPID}\n`);
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_DRY_RUN: '0',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_OPERATION: 'upgrade',
          CPAMP_VERSION: 'vnext',
          CPAMP_INSTALL_DIR: installDir,
          CPAMP_FAKE_NATIVE_ARCHIVE: release.archivePath,
          PATH: `${release.fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('does not belong to this CPA Manager Plus');
      expect(() => process.kill(unrelatedPID, 0)).not.toThrow();
      expect(readFileSync(path.join(installDir, 'cpa-manager-plus.pid'), 'utf8').trim()).toBe(
        String(unrelatedPID)
      );
      expect(readFileSync(legacy.runPath, 'utf8')).toBe(beforeRun);
      expect(readFileSync(legacy.dbPath, 'utf8')).toBe('existing-usage-data\n');
    } finally {
      if (Number.isInteger(unrelatedPID)) {
        try {
          process.kill(unrelatedPID, 'SIGTERM');
        } catch {
          // The unrelated fixture may already have exited.
        }
      }
      rmSync(installDir, { recursive: true, force: true });
      rmSync(release.fakeBin, { recursive: true, force: true });
      rmSync(release.fixtureDir, { recursive: true, force: true });
    }
  });

  it('does not start a duplicate legacy process when the old process refuses to stop', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const release = writeFakeNativeRelease();
    const legacy = writeLegacyNativeInstall(installDir);
    const oldProcess = spawn(legacy.runPath, {
      cwd: installDir,
      env: { ...process.env, FAKE_LEGACY_NATIVE_IGNORE_TERM: '1' },
      stdio: 'ignore',
    });
    const oldPID = oldProcess.pid;
    const beforeRun = readFileSync(legacy.runPath, 'utf8');

    try {
      expect(Number.isInteger(oldPID)).toBe(true);
      spawnSync('bash', ['-c', 'sleep 0.2'], { encoding: 'utf8' });
      writeFileSync(path.join(installDir, 'cpa-manager-plus.pid'), `${oldPID}\n`);
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_DRY_RUN: '0',
          CPAMP_NATIVE_STOP_ATTEMPTS: '1',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_OPERATION: 'upgrade',
          CPAMP_VERSION: 'vnext',
          CPAMP_INSTALL_DIR: installDir,
          CPAMP_FAKE_NATIVE_ARCHIVE: release.archivePath,
          PATH: `${release.fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('Failed to stop the existing native CPA Manager Plus process');
      expect(() => process.kill(oldPID, 0)).not.toThrow();
      expect(readFileSync(path.join(installDir, 'cpa-manager-plus.pid'), 'utf8').trim()).toBe(
        String(oldPID)
      );
      expect(readFileSync(legacy.runPath, 'utf8')).toBe(beforeRun);
      expect(readFileSync(legacy.dbPath, 'utf8')).toBe('existing-usage-data\n');
    } finally {
      if (Number.isInteger(oldPID)) {
        try {
          process.kill(oldPID, 'SIGKILL');
        } catch {
          // The legacy fixture may already have exited.
        }
      }
      rmSync(installDir, { recursive: true, force: true });
      rmSync(release.fakeBin, { recursive: true, force: true });
      rmSync(release.fixtureDir, { recursive: true, force: true });
    }
  });

  it('fails closed when a native upgrade has no PID file but its port is listening', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const release = writeFakeNativeRelease();
    const legacy = writeLegacyNativeInstall(installDir);
    const fakeLsof = path.join(release.fakeBin, 'lsof');
    const beforeRun = readFileSync(legacy.runPath, 'utf8');
    writeFileSync(
      fakeLsof,
      `#!/usr/bin/env bash\ncase "$*" in\n  *-iTCP:18317*) exit 0 ;;\n  *) exit 1 ;;\nesac\n`
    );
    chmodSync(fakeLsof, 0o755);

    try {
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_DRY_RUN: '0',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_OPERATION: 'upgrade',
          CPAMP_VERSION: 'vnext',
          CPAMP_INSTALL_DIR: installDir,
          CPAMP_FAKE_NATIVE_ARCHIVE: release.archivePath,
          PATH: `${release.fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain(
        'without an owned PID file. Stop the service and retry'
      );
      expect(readFileSync(legacy.runPath, 'utf8')).toBe(beforeRun);
      expect(readFileSync(legacy.dbPath, 'utf8')).toBe('existing-usage-data\n');
      expect(existsSync(legacy.cpaKeyPath)).toBe(true);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(release.fakeBin, { recursive: true, force: true });
      rmSync(release.fixtureDir, { recursive: true, force: true });
    }
  });

  it('restores legacy native data and entry files when the offline CPA import fails', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const release = writeFakeNativeRelease();
    const legacy = writeLegacyNativeInstall(installDir);
    const commandLog = path.join(installDir, 'native-command.log');
    const beforeRun = readFileSync(legacy.runPath, 'utf8');
    const beforeService = readFileSync(
      path.join(installDir, 'cpa-manager-plus.service'),
      'utf8'
    );
    const journalPath = `${legacy.dbPath}-journal`;
    writeFileSync(journalPath, 'existing-journal\n');

    try {
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_DRY_RUN: '0',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_OPERATION: 'upgrade',
          CPAMP_VERSION: 'vnext',
          CPAMP_INSTALL_DIR: installDir,
          CPAMP_FAKE_NATIVE_ARCHIVE: release.archivePath,
          FAKE_NATIVE_COMMAND_LOG: commandLog,
          FAKE_NATIVE_DB_PATH: legacy.dbPath,
          FAKE_NATIVE_JOURNAL_PATH: journalPath,
          FAKE_NATIVE_IMPORT_OK: '0',
          PATH: `${release.fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('CPA connection import failed');
      expect(readFileSync(legacy.dbPath, 'utf8')).toBe('existing-usage-data\n');
      expect(readFileSync(legacy.dataKeyPath, 'utf8')).toBe('existing-data-key\n');
      expect(readFileSync(journalPath, 'utf8')).toBe('existing-journal\n');
      expect(readFileSync(legacy.runPath, 'utf8')).toBe(beforeRun);
      expect(readFileSync(path.join(installDir, 'cpa-manager-plus.service'), 'utf8')).toBe(
        beforeService
      );
      expect(existsSync(legacy.cpaKeyPath)).toBe(true);
    } finally {
      stopNativeFixtureProcess(installDir);
      rmSync(installDir, { recursive: true, force: true });
      rmSync(release.fakeBin, { recursive: true, force: true });
      rmSync(release.fixtureDir, { recursive: true, force: true });
    }
  });

  it('rolls back a native upgrade on an unhandled set -e exit after data mutation', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const release = writeFakeNativeRelease();
    const legacy = writeLegacyNativeInstall(installDir);
    const commandLog = path.join(installDir, 'native-command.log');
    const importMarker = path.join(installDir, 'native-import-complete');
    const fakeMv = path.join(release.fakeBin, 'mv');
    const beforeRun = readFileSync(legacy.runPath, 'utf8');
    writeFileSync(
      fakeMv,
      `#!/usr/bin/env bash
if [ -e ${JSON.stringify(importMarker)} ]; then
  case "$*" in
    *run.sh*) exit 73 ;;
  esac
fi
exec /bin/mv "$@"
`
    );
    chmodSync(fakeMv, 0o755);

    try {
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_DRY_RUN: '0',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_OPERATION: 'upgrade',
          CPAMP_VERSION: 'vnext',
          CPAMP_INSTALL_DIR: installDir,
          CPAMP_FAKE_NATIVE_ARCHIVE: release.archivePath,
          FAKE_NATIVE_COMMAND_LOG: commandLog,
          FAKE_NATIVE_DB_PATH: legacy.dbPath,
          FAKE_NATIVE_IMPORT_MARKER: importMarker,
          PATH: `${release.fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(73);
      expect(readFileSync(legacy.dbPath, 'utf8')).toBe('existing-usage-data\n');
      expect(readFileSync(legacy.dataKeyPath, 'utf8')).toBe('existing-data-key\n');
      expect(readFileSync(legacy.runPath, 'utf8')).toBe(beforeRun);
      expect(existsSync(legacy.cpaKeyPath)).toBe(true);
    } finally {
      stopNativeFixtureProcess(installDir);
      rmSync(installDir, { recursive: true, force: true });
      rmSync(release.fakeBin, { recursive: true, force: true });
      rmSync(release.fixtureDir, { recursive: true, force: true });
    }
  });

  it('keeps a legacy CPA key outside the installer-managed secret path after import', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const externalDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-external-secret-'));
    const release = writeFakeNativeRelease();
    const legacy = writeLegacyNativeInstall(installDir);
    const externalKeyPath = path.join(externalDir, 'custom-cpa-key');
    const commandLog = path.join(installDir, 'native-command.log');
    const config = JSON.parse(readFileSync(legacy.configPath, 'utf8'));
    config.managementKeyFile = externalKeyPath;
    writeFileSync(legacy.configPath, `${JSON.stringify(config, null, 2)}\n`);
    writeFileSync(externalKeyPath, 'cpa_external_management_key\n');
    chmodSync(externalKeyPath, 0o640);
    rmSync(legacy.cpaKeyPath);

    try {
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_DRY_RUN: '0',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_OPERATION: 'upgrade',
          CPAMP_VERSION: 'vnext',
          CPAMP_INSTALL_DIR: installDir,
          CPAMP_FAKE_NATIVE_ARCHIVE: release.archivePath,
          FAKE_NATIVE_COMMAND_LOG: commandLog,
          FAKE_NATIVE_DB_PATH: legacy.dbPath,
          PATH: `${release.fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      expect(readFileSync(externalKeyPath, 'utf8')).toBe('cpa_external_management_key\n');
      expect(statSync(externalKeyPath).mode & 0o777).toBe(0o640);
      expect(combinedOutput(result)).toContain(externalKeyPath);
    } finally {
      stopNativeFixtureProcess(installDir);
      rmSync(installDir, { recursive: true, force: true });
      rmSync(externalDir, { recursive: true, force: true });
      rmSync(release.fakeBin, { recursive: true, force: true });
      rmSync(release.fixtureDir, { recursive: true, force: true });
    }
  });

  it.each([
    {
      name: 'health check',
      env: { FAKE_NATIVE_HEALTH_OK: '0', FAKE_NATIVE_AUTH_OK: '1' },
      expected: 'did not become healthy',
    },
    {
      name: 'admin authentication',
      env: { FAKE_NATIVE_HEALTH_OK: '1', FAKE_NATIVE_AUTH_OK: '0' },
      expected: 'admin key verification failed',
    },
    {
      name: 'CPA connection validation',
      env: {
        FAKE_NATIVE_HEALTH_OK: '1',
        FAKE_NATIVE_AUTH_OK: '1',
        FAKE_NATIVE_CPA_OK: '0',
      },
      expected: 'CPA connection validation failed',
    },
  ])('rolls back a legacy native upgrade when $name fails', ({ env, expected }) => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const release = writeFakeNativeRelease();
    const legacy = writeLegacyNativeInstall(installDir);
    const commandLog = path.join(installDir, 'native-command.log');
    const beforeRun = readFileSync(legacy.runPath, 'utf8');
    const beforeService = readFileSync(
      path.join(installDir, 'cpa-manager-plus.service'),
      'utf8'
    );

    try {
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_DRY_RUN: '0',
          CPAMP_NATIVE_HEALTH_ATTEMPTS: '1',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_OPERATION: 'upgrade',
          CPAMP_VERSION: 'vnext',
          CPAMP_INSTALL_DIR: installDir,
          CPAMP_FAKE_NATIVE_ARCHIVE: release.archivePath,
          FAKE_NATIVE_COMMAND_LOG: commandLog,
          FAKE_NATIVE_DB_PATH: legacy.dbPath,
          PATH: `${release.fakeBin}${path.delimiter}${process.env.PATH || ''}`,
          ...env,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain(expected);
      expect(readFileSync(legacy.dbPath, 'utf8')).toBe('existing-usage-data\n');
      expect(readFileSync(legacy.dataKeyPath, 'utf8')).toBe('existing-data-key\n');
      expect(readFileSync(legacy.runPath, 'utf8')).toBe(beforeRun);
      expect(readFileSync(path.join(installDir, 'cpa-manager-plus.service'), 'utf8')).toBe(
        beforeService
      );
      expect(existsSync(legacy.cpaKeyPath)).toBe(true);
    } finally {
      stopNativeFixtureProcess(installDir);
      rmSync(installDir, { recursive: true, force: true });
      rmSync(release.fakeBin, { recursive: true, force: true });
      rmSync(release.fixtureDir, { recursive: true, force: true });
    }
  });

  it('does not restore native data while the replacement process still owns the database', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const release = writeFakeNativeRelease();
    const legacy = writeLegacyNativeInstall(installDir);
    const commandLog = path.join(installDir, 'native-command.log');
    const beforeRun = readFileSync(legacy.runPath, 'utf8');

    try {
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_DRY_RUN: '0',
          CPAMP_NATIVE_HEALTH_ATTEMPTS: '1',
          CPAMP_NATIVE_STOP_ATTEMPTS: '1',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_OPERATION: 'upgrade',
          CPAMP_VERSION: 'vnext',
          CPAMP_INSTALL_DIR: installDir,
          CPAMP_FAKE_NATIVE_ARCHIVE: release.archivePath,
          FAKE_NATIVE_COMMAND_LOG: commandLog,
          FAKE_NATIVE_DB_PATH: legacy.dbPath,
          FAKE_NATIVE_IMPORT_OK: '1',
          FAKE_NATIVE_IGNORE_TERM: '1',
          FAKE_NATIVE_HEALTH_OK: '0',
          PATH: `${release.fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('did not become healthy');
      expect(combinedOutput(result)).toContain('Automatic native upgrade rollback did not fully succeed');
      expect(readFileSync(legacy.dbPath, 'utf8')).toContain('existing-usage-data\nimport-attempt\n');
      expect(readFileSync(legacy.dbPath, 'utf8')).not.toBe('existing-usage-data\n');
      expect(readFileSync(legacy.runPath, 'utf8')).not.toBe(beforeRun);
      const pidPath = path.join(installDir, 'cpa-manager-plus.pid');
      const pid = Number(readFileSync(pidPath, 'utf8').trim());
      expect(Number.isInteger(pid)).toBe(true);
      expect(() => process.kill(pid, 0)).not.toThrow();
    } finally {
      stopNativeFixtureProcess(installDir);
      rmSync(installDir, { recursive: true, force: true });
      rmSync(release.fakeBin, { recursive: true, force: true });
      rmSync(release.fixtureDir, { recursive: true, force: true });
    }
  });

  it('keeps native runtime config secret-free and prints the one-time import command', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));

    try {
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_SKIP_EXECUTE: '1',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_MODE: 'cpamp',
          CPAMP_DEPLOY_METHOD: 'native',
          CPAMP_CPA_CONNECTION_MODE: 'env',
          CPAMP_CPA_URL: 'http://127.0.0.1:8317',
          CPAMP_CPA_MANAGEMENT_KEY: 'cpa_native_import_key',
          CPAMP_VERSION: 'v1.8.1',
          CPAMP_INSTALL_DIR: installDir,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      const platform = process.platform === 'darwin' ? 'darwin' : 'linux';
      const arch = process.arch === 'arm64' ? 'arm64' : 'amd64';
      const configPath = path.join(
        installDir,
        'runtime',
        `cpa-manager-plus_v1.8.1_${platform}_${arch}`,
        'config.json'
      );
      const config = readFileSync(configPath, 'utf8');
      expect(config).not.toContain('cpaUpstreamUrl');
      expect(config).not.toContain('managementKeyFile');
      expect(readFileSync(path.join(installDir, 'run.sh'), 'utf8')).toContain(
        'unset CPA_UPSTREAM_URL CPA_MANAGEMENT_KEY CPA_MANAGEMENT_KEY_FILE'
      );
      expect(result.stdout).toContain('store-cpa-connection');
      expect(result.stdout).toContain('CPA_MANAGEMENT_KEY_FILE=/dev/null');
      expect(result.stdout).toContain('--data-key-path');
      expect(result.stdout.indexOf('store-cpa-connection')).toBeLessThan(
        result.stdout.indexOf('nohup')
      );
      expect(result.stdout.indexOf('nohup')).toBeLessThan(result.stdout.indexOf('/health'));
      expect(result.stdout.indexOf('/health')).toBeLessThan(result.stdout.indexOf('/status'));
      expect(result.stdout.indexOf('/status')).toBeLessThan(result.stdout.indexOf('rm -f'));
      expect(existsSync(path.join(installDir, 'secrets/cpa-management-key'))).toBe(true);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
    }
  });

  it('keeps the native temporary CPA key when admin verification fails', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const fixtureDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-fixture-'));
    const platform = process.platform === 'darwin' ? 'darwin' : 'linux';
    const arch = process.arch === 'arm64' ? 'arm64' : 'amd64';
    const packageName = `cpa-manager-plus_vtest_${platform}_${arch}`;
    const packageDir = path.join(fixtureDir, packageName);
    const archivePath = path.join(fixtureDir, `${packageName}.tar.gz`);
    let nativePid;

    try {
      mkdirSync(packageDir, { recursive: true });
      const fakeBinary = path.join(packageDir, 'cpa-manager-plus');
      writeFileSync(
        fakeBinary,
        `#!/usr/bin/env bash
set -euo pipefail
if [ "\${1:-}" = "store-cpa-connection" ]; then
  exit 0
fi
trap 'exit 0' TERM INT
while true; do
  sleep 1
done
`
      );
      chmodSync(fakeBinary, 0o755);
      const tarResult = spawnSync('tar', ['-czf', archivePath, '-C', fixtureDir, packageName], {
        cwd: repoRoot,
        encoding: 'utf8',
      });
      expect(tarResult.status).toBe(0);

      const fakeCurl = path.join(fakeBin, 'curl');
      writeFileSync(
        fakeCurl,
        `#!/usr/bin/env bash
set -euo pipefail
for arg in "$@"; do
  case "$arg" in
    https://github.com/seakee/CPA-Manager-Plus/releases/latest)
      printf 'https://github.com/seakee/CPA-Manager-Plus/releases/tag/vtest'
      exit 0
      ;;
    */health) exit 0 ;;
    */status) exit 22 ;;
  esac
done
out=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-o" ]; then
    out="$arg"
    break
  fi
  prev="$arg"
done
if [ -n "$out" ]; then
  cp "$CPAMP_FAKE_NATIVE_ARCHIVE" "$out"
  exit 0
fi
exit 22
`
      );
      chmodSync(fakeCurl, 0o755);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_MODE: 'cpamp',
          CPAMP_DEPLOY_METHOD: 'native',
          CPAMP_CPA_CONNECTION_MODE: 'env',
          CPAMP_CPA_URL: 'http://127.0.0.1:8317',
          CPAMP_CPA_MANAGEMENT_KEY: 'cpa_native_import_key',
          CPAMP_INSTALL_DIR: installDir,
          CPAMP_FAKE_NATIVE_ARCHIVE: archivePath,
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      const pidPath = path.join(installDir, 'cpa-manager-plus.pid');
      if (existsSync(pidPath)) {
        nativePid = Number(readFileSync(pidPath, 'utf8').trim());
      }
      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('admin key verification failed');
      expect(existsSync(path.join(installDir, 'secrets/cpa-management-key'))).toBe(true);
    } finally {
      if (Number.isInteger(nativePid) && nativePid > 0) {
        try {
          process.kill(nativePid, 'SIGTERM');
        } catch {
          // The fake process may already have exited.
        }
      }
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(fixtureDir, { recursive: true, force: true });
    }
  });

  it('fails native installs when the started process exits before health is ready', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const fixtureDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-fixture-'));
    const platform = process.platform === 'darwin' ? 'darwin' : 'linux';
    const arch = process.arch === 'arm64' ? 'arm64' : 'amd64';
    const packageName = `cpa-manager-plus_vtest_${platform}_${arch}`;
    const packageDir = path.join(fixtureDir, packageName);
    const archivePath = path.join(fixtureDir, `${packageName}.tar.gz`);

    try {
      mkdirSync(packageDir, { recursive: true });
      const fakeBinary = path.join(packageDir, 'cpa-manager-plus');
      writeFileSync(
        fakeBinary,
        '#!/usr/bin/env bash\necho "fake native process exited" >&2\nexit 42\n'
      );
      chmodSync(fakeBinary, 0o755);
      const tarResult = spawnSync('tar', ['-czf', archivePath, '-C', fixtureDir, packageName], {
        cwd: repoRoot,
        encoding: 'utf8',
      });
      expect(tarResult.status).toBe(0);

      const fakeCurl = path.join(fakeBin, 'curl');
      writeFileSync(
        fakeCurl,
        `#!/usr/bin/env bash
set -euo pipefail
for arg in "$@"; do
  if [ "$arg" = "https://github.com/seakee/CPA-Manager-Plus/releases/latest" ]; then
    printf 'https://github.com/seakee/CPA-Manager-Plus/releases/tag/vtest'
    exit 0
  fi
done
out=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-o" ]; then
    out="$arg"
    break
  fi
  prev="$arg"
done
if [ -n "$out" ]; then
  cp "$CPAMP_FAKE_NATIVE_ARCHIVE" "$out"
  exit 0
fi
exit 22
`
      );
      chmodSync(fakeCurl, 0o755);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_SKIP_EXECUTE: '0',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_MODE: 'cpamp',
          CPAMP_DEPLOY_METHOD: 'native',
          CPAMP_CPA_CONNECTION_MODE: 'setup',
          CPAMP_INSTALL_DIR: installDir,
          CPAMP_FAKE_NATIVE_ARCHIVE: archivePath,
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain(
        'Native CPAMP process exited before becoming healthy'
      );
      expect(combinedOutput(result)).toContain('fake native process exited');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(fixtureDir, { recursive: true, force: true });
    }
  });

  it('generates a Linux systemd unit for native installs', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));

    try {
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_SKIP_EXECUTE: '1',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_MODE: 'cpamp',
          CPAMP_DEPLOY_METHOD: 'native',
          CPAMP_CPA_CONNECTION_MODE: 'setup',
          CPAMP_VERSION: 'v1.8.1',
          CPAMP_INSTALL_DIR: installDir,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);

      if (process.platform === 'linux') {
        const service = readFileSync(path.join(installDir, 'cpa-manager-plus.service'), 'utf8');

        expect(service).toContain('[Unit]');
        expect(service).toContain('ExecStart=');
        expect(service).toContain('/cpa-manager-plus');
        expect(service).toContain('Environment="CPA_UPSTREAM_URL="');
        expect(service).toContain('Environment="CPA_MANAGEMENT_KEY="');
        expect(service).toContain('Environment="CPA_MANAGEMENT_KEY_FILE="');
      }
    } finally {
      rmSync(installDir, { recursive: true, force: true });
    }
  });

  it('quotes native systemd paths containing spaces and percent signs', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp installer %-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const fakeUname = path.join(fakeBin, 'uname');
    writeFileSync(
      fakeUname,
      '#!/usr/bin/env bash\ncase "${1:-}" in -s) echo Linux ;; -m) echo x86_64 ;; *) exit 1 ;; esac\n'
    );
    chmodSync(fakeUname, 0o755);

    try {
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_SKIP_EXECUTE: '1',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_MODE: 'cpamp',
          CPAMP_DEPLOY_METHOD: 'native',
          CPAMP_CPA_CONNECTION_MODE: 'setup',
          CPAMP_VERSION: 'v1.8.1',
          CPAMP_INSTALL_DIR: installDir,
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      const service = readFileSync(path.join(installDir, 'cpa-manager-plus.service'), 'utf8');
      const binaryDir = path.join(
        installDir,
        'runtime',
        'cpa-manager-plus_v1.8.1_linux_amd64'
      );
      const escapedBinaryDir = binaryDir.replaceAll('%', '%%');
      expect(service).toContain(`WorkingDirectory="${escapedBinaryDir}"`);
      expect(service).toContain(`ExecStart="${escapedBinaryDir}/cpa-manager-plus"`);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
    }
  });
});
