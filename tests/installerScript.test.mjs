import { spawn, spawnSync } from 'node:child_process';
import {
  chmodSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  realpathSync,
  readdirSync,
  rmSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const installerPath = path.join(repoRoot, 'bin/install-cpamp.sh');

const combinedOutput = (result) => `${result.stdout}\n${result.stderr}`;

const installerTextContents = (root) => {
  const contents = [];
  const visit = (directory) => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const candidate = path.join(directory, entry.name);
      if (entry.isDirectory()) {
        visit(candidate);
      } else if (entry.isFile() && !/(?:\.sqlite(?:-(?:wal|shm|journal))?)$/.test(entry.name)) {
        contents.push(readFileSync(candidate, 'utf8'));
      }
    }
  };
  visit(root);
  return contents.join('\n');
};

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
if [ "$1" = "compose" ] && [ "\${2:-}" = "config" ] && [ "\${FAKE_DOCKER_CONFIG_OK:-1}" != "1" ]; then
  exit 1
fi
if [ "$1" = "compose" ] && [ "\${2:-}" = "config" ] && [ -n "\${FAKE_DOCKER_CONFIG_OUTPUT_FILE:-}" ]; then
  cat "\$FAKE_DOCKER_CONFIG_OUTPUT_FILE"
  exit 0
fi
if [ "$1" = "compose" ] && [ "\${2:-}" = "exec" ]; then
  case "$*" in
    *'/v0/management/cpa-connection/validate'*)
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
  if [[ "$*" == *'--entrypoint /bin/sh cpa-manager-plus'* ]] && [ "\${FAKE_DOCKER_PATH_ACCESS_OK:-1}" != "1" ]; then
    exit 1
  fi
  run_db_path=""
  run_data_key_path=""
  previous=""
  for argument in "$@"; do
    if [ "$previous" = "--db-path" ]; then run_db_path="$argument"; fi
    if [ "$previous" = "--data-key-path" ]; then run_data_key_path="$argument"; fi
    previous="$argument"
  done
  if [[ "$*" == *'store-cpa-connection'* || "$*" == *'manager-data-snapshot create'* || "$*" == *'manager-data-snapshot restore'* ]]; then
    if [ -n "\${FAKE_DOCKER_EXPECTED_DB_PATH:-}" ] && [ "$run_db_path" != "\${FAKE_DOCKER_EXPECTED_DB_PATH}" ]; then
      exit 97
    fi
    if [ -n "\${FAKE_DOCKER_EXPECTED_DATA_KEY_PATH:-}" ] && [ "$run_data_key_path" != "\${FAKE_DOCKER_EXPECTED_DATA_KEY_PATH}" ]; then
      exit 98
    fi
  fi
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

const writeFakeRmThatFailsForMigrationBackups = (dir) => {
  const fakeRm = path.join(dir, 'rm');
  writeFileSync(
    fakeRm,
    `#!/usr/bin/env bash
set -eu
for argument in "$@"; do
  case "$argument" in
    *cpa-key-migration.bak.*) exit 1 ;;
  esac
done
exec /bin/rm "$@"
`
  );
  chmodSync(fakeRm, 0o755);
};

const nativePlatform = process.platform === 'darwin' ? 'darwin' : 'linux';
const nativeArch = process.arch === 'arm64' ? 'arm64' : 'amd64';

const writeLegacyNativeInstall = (installDir, options = {}) => {
  const packageName = `cpa-manager-plus_vold_${nativePlatform}_${nativeArch}`;
  const binaryDir = path.join(installDir, 'runtime', packageName);
  const configPath = path.join(binaryDir, 'config.json');
  const runPath = path.join(installDir, 'run.sh');
  const adminKeyPath = path.join(installDir, 'secrets', 'cpamp-admin-key');
  const cpaKeyPath = path.join(installDir, 'secrets', 'cpa-management-key');
  const dbPath = options.dbPath || path.join(installDir, 'data', 'usage.sqlite');
  const dataKeyPath = options.dataKeyPath || path.join(installDir, 'data', 'data.key');
  const configDataDir = options.configDataDir || '../../data';
  const configDataKeyPath = options.configDataKeyPath || '../../data/data.key';
  const runEnvironment = options.runEnvironment || '';
  const serviceEnvironment = options.serviceEnvironment || '';

  mkdirSync(binaryDir, { recursive: true });
  mkdirSync(path.dirname(adminKeyPath), { recursive: true });
  mkdirSync(path.dirname(dbPath), { recursive: true });
  mkdirSync(path.dirname(dataKeyPath), { recursive: true });
  writeFileSync(
    path.join(binaryDir, 'cpa-manager-plus'),
    `#!/usr/bin/env bash
if [ "\${FAKE_LEGACY_NATIVE_IGNORE_TERM:-0}" = "1" ]; then
  trap ':' TERM INT
  while true; do sleep 1; done
fi
trap 'exit 0' TERM INT
if [ -n "\${FAKE_LEGACY_NATIVE_START_MARKER:-}" ]; then
  printf 'start\n' >> "\$FAKE_LEGACY_NATIVE_START_MARKER"
fi
while true; do read -r -t 1 _ || true; done
`
  );
  chmodSync(path.join(binaryDir, 'cpa-manager-plus'), 0o755);
  writeFileSync(
    configPath,
    `${JSON.stringify(
      {
        httpAddr: '0.0.0.0:18317',
        dataDir: configDataDir,
        adminKeyFile: '../../secrets/cpamp-admin-key',
        dataKeyPath: configDataKeyPath,
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
    `#!/usr/bin/env bash\nset -euo pipefail\n${runEnvironment}cd "${binaryDir}"\nexec ./cpa-manager-plus\n`
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
    `[Service]\nWorkingDirectory=${binaryDir}\n${serviceEnvironment}ExecStart=${binaryDir}/cpa-manager-plus\n`
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

const writeCustomDockerInstall = (
  installDir,
  { environmentSyntax = 'mapping', volumeSyntax = 'short', withVolume = true } = {}
) => {
  const databasePath = path.join(installDir, 'fake-container', 'custom.sqlite');
  const dataKeyPath = path.join(installDir, 'fake-container', 'custom.key');
  const defaultDatabasePath = path.join(installDir, 'fake-container', 'usage.sqlite');
  const defaultDataKeyPath = path.join(installDir, 'fake-container', 'data.key');
  const keyPath = path.join(installDir, 'secrets', 'cpa-management-key');
  const databaseExpression = '\${CUSTOM_DB_PATH:-/data/usage.sqlite}';
  const dataKeyExpression = '\${CUSTOM_DATA_KEY_PATH:-/data/data.key}';
  const environment =
    environmentSyntax === 'list'
      ? `      - CPA_UPSTREAM_URL=\${CPA_UPSTREAM_URL}
      - CPA_MANAGEMENT_KEY_FILE=/run/secrets/cpa_management_key
      - USAGE_DB_PATH=${databaseExpression}
      - CPA_MANAGER_DATA_KEY_PATH=${dataKeyExpression}
`
      : `      CPA_UPSTREAM_URL: "\${CPA_UPSTREAM_URL}"
      CPA_MANAGEMENT_KEY_FILE: "/run/secrets/cpa_management_key"
      USAGE_DB_PATH: "${databaseExpression}"
      CPA_MANAGER_DATA_KEY_PATH: "${dataKeyExpression}"
`;
  const volume = !withVolume
    ? ''
    : volumeSyntax === 'long'
      ? '    volumes:\n      - type: volume\n        source: cpa-manager-plus-data\n        target: /data\n'
      : volumeSyntax === 'exact-file'
        ? '    volumes:\n      - ./fake-container/custom.sqlite:/data/custom.sqlite\n      - ./fake-container/custom.key:/data/custom.key\n'
        : '    volumes:\n      - cpa-manager-plus-data:/data\n';
  const composeContent = `services:
  cpa-manager-plus:
    image: \${CPAMP_IMAGE}
    environment:
${environment}${volume}    secrets:
      - cpamp_admin_key
      - cpa_management_key
secrets:
  cpamp_admin_key:
    file: ./secrets/cpamp-admin-key
  cpa_management_key:
    file: ./secrets/cpa-management-key
`;
  const envContent =
    'COMPOSE_PROJECT_NAME=cpamp\n' +
    'CPAMP_IMAGE=example/cpamp:v1\n' +
    'CPAMP_PORT=18317\n' +
    'CPA_UPSTREAM_URL=http://host.docker.internal:8317\n' +
    'CUSTOM_DB_PATH=/data/custom.sqlite\n' +
    'CUSTOM_DATA_KEY_PATH=/data/custom.key\n';

  mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
  mkdirSync(path.dirname(databasePath), { recursive: true });
  writeFileSync(path.join(installDir, '.env'), envContent);
  writeFileSync(path.join(installDir, 'compose.yaml'), composeContent);
  writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
  writeFileSync(keyPath, 'cpa_custom_management_key\n');
  writeFileSync(databasePath, 'custom-database-before\n');
  writeFileSync(dataKeyPath, 'custom-data-key-before\n');
  writeFileSync(defaultDatabasePath, 'default-database-sentinel\n');
  writeFileSync(defaultDataKeyPath, 'default-data-key-sentinel\n');

  return {
    databasePath,
    dataKeyPath,
    defaultDatabasePath,
    defaultDataKeyPath,
    keyPath,
    composeContent,
    envContent,
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
if [ "\${1:-}" = "manager-data-snapshot" ]; then
  action="\${2:-}"
  shift 2
  snapshot_dir=""
  db_path=""
  data_key_path=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --snapshot-dir) snapshot_dir="$2"; shift 2 ;;
      --db-path) db_path="$2"; shift 2 ;;
      --data-key-path) data_key_path="$2"; shift 2 ;;
      *) exit 43 ;;
    esac
  done
  if [ "$action" = "create" ]; then
    [ "\${FAKE_NATIVE_SNAPSHOT_CREATE_OK:-1}" = "1" ]
    mkdir -p "$snapshot_dir"
    for suffix in '' -wal -shm -journal; do
      source="\${db_path}\${suffix}"
      target="$snapshot_dir/database\${suffix}"
      if [ -e "$source" ]; then
        cp "$source" "$target"
      else
        : > "$target.missing"
      fi
    done
    if [ -e "$data_key_path" ]; then
      cp "$data_key_path" "$snapshot_dir/data-key"
    else
      : > "$snapshot_dir/data-key.missing"
    fi
  elif [ "$action" = "restore" ]; then
    [ "\${FAKE_NATIVE_SNAPSHOT_RESTORE_OK:-1}" = "1" ]
    for suffix in '' -wal -shm -journal; do
      target="\${db_path}\${suffix}"
      source="$snapshot_dir/database\${suffix}"
      if [ -e "$source.missing" ]; then
        rm -f "$target"
      else
        cp "$source" "$target"
      fi
    done
    if [ "\${FAKE_NATIVE_SNAPSHOT_RESTORE_FAIL_AFTER_DATABASE:-0}" = "1" ]; then
      exit 45
    fi
    if [ -e "$snapshot_dir/data-key.missing" ]; then
      rm -f "$data_key_path"
    else
      cp "$snapshot_dir/data-key" "$data_key_path"
    fi
  elif [ "$action" = "delete" ]; then
    [ "\${FAKE_NATIVE_SNAPSHOT_DELETE_OK:-1}" = "1" ]
    rm -f "$snapshot_dir"/*
    rmdir "$snapshot_dir"
  else
    exit 44
  fi
  exit 0
fi
if [ "\${1:-}" = "store-cpa-connection" ]; then
  printf '%s\n' "$*" >> "$FAKE_NATIVE_COMMAND_LOG"
  data_key_path=""
  previous=""
  for arg in "$@"; do
    if [ "$previous" = "--data-key-path" ]; then data_key_path="$arg"; fi
    previous="$arg"
  done
  if [ -n "$data_key_path" ] && [ ! -e "$data_key_path" ]; then
    mkdir -p "$(dirname "$data_key_path")"
    printf 'fake-data-key\n' > "$data_key_path"
  fi
  printf 'import-attempt\n' >> "$FAKE_NATIVE_DB_PATH"
  if [ -n "\${FAKE_NATIVE_IMPORT_MARKER:-}" ]; then
    : > "$FAKE_NATIVE_IMPORT_MARKER"
  fi
  if [ -n "\${FAKE_NATIVE_JOURNAL_PATH:-}" ]; then
    printf 'import-attempt\n' >> "$FAKE_NATIVE_JOURNAL_PATH"
  fi
  if [ "\${FAKE_NATIVE_MUTATE_ALL_DATA:-0}" = "1" ]; then
    for suffix in -wal -shm -journal; do
      printf 'import-attempt\n' >> "\${FAKE_NATIVE_DB_PATH}\${suffix}"
    done
    printf 'import-attempt\n' >> "$FAKE_NATIVE_DATA_KEY_PATH"
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
  node -e 'const fs=require("fs"); const value=JSON.parse(fs.readFileSync(process.argv[1], "utf8")); for (const key of Object.keys(value)) { const folded=key.toLowerCase(); if (folded === "cpaupstreamurl" || folded === "managementkeyfile") delete value[key]; } fs.writeFileSync(process.argv[2], JSON.stringify(value, null, 2)+"\\n");' "$input" "$output"
  exit 0
fi
if [ "\${FAKE_NATIVE_IGNORE_TERM:-0}" = "1" ]; then
  trap ':' TERM INT
else
  trap 'if [ -n "\${FAKE_NATIVE_STOP_MARKER:-}" ]; then printf "stop\\n" >> "\$FAKE_NATIVE_STOP_MARKER"; fi; exit 0' TERM INT
fi
if [ -n "\${FAKE_NATIVE_START_MARKER:-}" ]; then
  printf 'start\\n' >> "\$FAKE_NATIVE_START_MARKER"
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
      if [ -n "\${FAKE_NATIVE_EMPTY_PID_FILE:-}" ]; then
        : > "\$FAKE_NATIVE_EMPTY_PID_FILE"
      fi
      [ "\${FAKE_NATIVE_HEALTH_OK:-1}" = "1" ]
      exit
      ;;
    */status)
      [ "\${FAKE_NATIVE_AUTH_OK:-1}" = "1" ]
      exit
      ;;
    */v0/management/cpa-connection/validate)
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
  const realPs = ['/bin/ps', '/usr/bin/ps'].find((candidate) => existsSync(candidate));
  const fakePs = path.join(fakeBin, 'ps');
  writeFileSync(
    fakePs,
    `#!/usr/bin/env bash
set -euo pipefail
pid=""
format=""
previous=""
for arg in "$@"; do
  if [ "$previous" = "-p" ]; then pid="$arg"; fi
  if [ "$previous" = "-o" ]; then format="$arg"; fi
  previous="$arg"
done
  case "$format" in
  lstart=)
    printf '%s\\n' "\${FAKE_NATIVE_PROCESS_START:-fake-native-process-start}"
    exit 0
    ;;
  stat=)
    ${realPs ? `exec ${realPs} -p "$pid" -o stat=` : `printf 'S\\n'`}
    exit 0
    ;;
  command=)
    printf './cpa-manager-plus\\n'
    exit 0
    ;;
esac
exit 1
`
  );
  chmodSync(fakePs, 0o755);
  return { fakeBin, fixtureDir, archivePath, packageName };
};

const stopNativeFixtureProcess = (installDir) => {
  const pidPath = path.join(installDir, 'cpa-manager-plus.pid');
  if (!existsSync(pidPath)) return;
  let pidContents = '';
  try {
    pidContents = readFileSync(pidPath, 'utf8');
  } catch {
    return;
  }
  const pid = Number(pidContents.trim());
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
      expect(result.stdout).toContain("--post-data=''");
      expect(result.stdout).not.toContain('--method=POST');
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

  it.each([
    { environmentSyntax: 'mapping', volumeSyntax: 'short' },
    { environmentSyntax: 'list', volumeSyntax: 'long' },
  ])(
    'uses the resolved custom Docker database and data-key paths ($environmentSyntax/$volumeSyntax)',
    ({ environmentSyntax, volumeSyntax }) => {
      const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
      const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
      const dockerLog = path.join(
        os.tmpdir(),
        `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
      );
      const snapshotStore = path.join(installDir, 'fake-docker-snapshot');
      const fixture = writeCustomDockerInstall(installDir, { environmentSyntax, volumeSyntax });

      try {
        writeFileSync(`${fixture.databasePath}-wal`, 'custom-wal-before\n');
        writeFileSync(`${fixture.databasePath}-shm`, 'custom-shm-before\n');
        writeFileSync(`${fixture.databasePath}-journal`, 'custom-journal-before\n');
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
            FAKE_DOCKER_DB_PATH: fixture.databasePath,
            FAKE_DOCKER_DATA_KEY_PATH: fixture.dataKeyPath,
            FAKE_DOCKER_EXPECTED_DB_PATH: '/data/custom.sqlite',
            FAKE_DOCKER_EXPECTED_DATA_KEY_PATH: '/data/custom.key',
            FAKE_DOCKER_MUTATE_ALL_DATA: '1',
            FAKE_DOCKER_SNAPSHOT_STORE: snapshotStore,
            PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
          },
          encoding: 'utf8',
        });

        expect(result.status).toBe(0);
        const calls = readFileSync(dockerLog, 'utf8');
        expect(calls).toContain('--db-path /data/custom.sqlite');
        expect(calls).toContain('--data-key-path /data/custom.key');
        expect(calls).not.toContain('--db-path /data/usage.sqlite');
        expect(calls).not.toContain('--data-key-path /data/data.key');
        expect(readFileSync(fixture.databasePath, 'utf8')).toContain('import-attempt');
        expect(readFileSync(`${fixture.databasePath}-wal`, 'utf8')).toContain('import-attempt');
        expect(readFileSync(`${fixture.databasePath}-shm`, 'utf8')).toContain('import-attempt');
        expect(readFileSync(`${fixture.databasePath}-journal`, 'utf8')).toContain('import-attempt');
        expect(readFileSync(fixture.dataKeyPath, 'utf8')).toContain('import-attempt');
        expect(readFileSync(fixture.defaultDatabasePath, 'utf8')).toBe(
          'default-database-sentinel\n'
        );
        expect(readFileSync(fixture.defaultDataKeyPath, 'utf8')).toBe(
          'default-data-key-sentinel\n'
        );
        expect(existsSync(fixture.keyPath)).toBe(false);
        expect(existsSync(path.join(installDir, 'secrets/cpa-connection-import.pending'))).toBe(
          false
        );
        expect(existsSync(snapshotStore)).toBe(false);
        expect(readFileSync(path.join(installDir, 'compose.yaml'), 'utf8')).not.toContain(
          'CPA_MANAGEMENT_KEY_FILE'
        );
      } finally {
        rmSync(installDir, { recursive: true, force: true });
        rmSync(fakeBin, { recursive: true, force: true });
        rmSync(dockerLog, { force: true });
      }
    }
  );

  it('uses the Docker Compose resolved service configuration for custom paths', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const resolvedConfigPath = path.join(installDir, 'resolved-compose.yaml');
    const snapshotStore = path.join(installDir, 'fake-docker-snapshot');
    const fixture = writeCustomDockerInstall(installDir);
    writeFileSync(
      resolvedConfigPath,
      `name: cpamp
services:
  cpa-manager-plus:
    image: example/cpamp:v1
    environment:
      CPA_UPSTREAM_URL: http://host.docker.internal:8317
      CPA_MANAGEMENT_KEY_FILE: /run/secrets/cpa_management_key
      USAGE_DB_PATH: /data/custom.sqlite
      CPA_MANAGER_DATA_KEY_PATH: /data/custom.key
    volumes:
      - type: volume
        source: cpa-manager-plus-data
        target: /data
`
    );

    try {
      writeFileSync(`${fixture.databasePath}-wal`, 'custom-wal-before\n');
      writeFileSync(`${fixture.databasePath}-shm`, 'custom-shm-before\n');
      writeFileSync(`${fixture.databasePath}-journal`, 'custom-journal-before\n');
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
          FAKE_DOCKER_CONFIG_OUTPUT_FILE: resolvedConfigPath,
          FAKE_DOCKER_DB_PATH: fixture.databasePath,
          FAKE_DOCKER_DATA_KEY_PATH: fixture.dataKeyPath,
          FAKE_DOCKER_EXPECTED_DB_PATH: '/data/custom.sqlite',
          FAKE_DOCKER_EXPECTED_DATA_KEY_PATH: '/data/custom.key',
          FAKE_DOCKER_MUTATE_ALL_DATA: '1',
          FAKE_DOCKER_SNAPSHOT_STORE: snapshotStore,
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      const calls = readFileSync(dockerLog, 'utf8');
      expect(calls).toContain('compose config');
      expect(calls).toContain('--db-path /data/custom.sqlite');
      expect(calls).toContain('--data-key-path /data/custom.key');
      expect(calls).not.toContain('--db-path /data/usage.sqlite');
      expect(calls).not.toContain('--data-key-path /data/data.key');
      expect(readFileSync(fixture.databasePath, 'utf8')).toContain('import-attempt');
      expect(readFileSync(fixture.dataKeyPath, 'utf8')).toContain('import-attempt');
      expect(readFileSync(fixture.defaultDatabasePath, 'utf8')).toBe('default-database-sentinel\n');
      expect(readFileSync(fixture.defaultDataKeyPath, 'utf8')).toBe('default-data-key-sentinel\n');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('treats resolved YAML null paths as unset and uses Manager defaults', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const resolvedConfigPath = path.join(installDir, 'resolved-compose.yaml');
    const snapshotStore = path.join(installDir, 'fake-docker-snapshot');
    const fixture = writeCustomDockerInstall(installDir);
    writeFileSync(
      resolvedConfigPath,
      `name: cpamp
services:
  cpa-manager-plus:
    image: example/cpamp:v1
    environment:
      CPA_UPSTREAM_URL: http://host.docker.internal:8317
      CPA_MANAGEMENT_KEY_FILE: /run/secrets/cpa_management_key
      USAGE_DB_PATH: null
      CPA_MANAGER_DATA_KEY_PATH: ~
    volumes:
      - type: volume
        source: cpa-manager-plus-data
        target: /data
`
    );

    try {
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
          FAKE_DOCKER_CONFIG_OUTPUT_FILE: resolvedConfigPath,
          FAKE_DOCKER_DB_PATH: fixture.defaultDatabasePath,
          FAKE_DOCKER_DATA_KEY_PATH: fixture.defaultDataKeyPath,
          FAKE_DOCKER_EXPECTED_DB_PATH: '/data/usage.sqlite',
          FAKE_DOCKER_EXPECTED_DATA_KEY_PATH: '/data/data.key',
          FAKE_DOCKER_MUTATE_ALL_DATA: '1',
          FAKE_DOCKER_SNAPSHOT_STORE: snapshotStore,
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      const calls = readFileSync(dockerLog, 'utf8');
      expect(calls).toContain('--db-path /data/usage.sqlite');
      expect(calls).toContain('--data-key-path /data/data.key');
      expect(readFileSync(fixture.defaultDatabasePath, 'utf8')).toContain('import-attempt');
      expect(readFileSync(fixture.defaultDataKeyPath, 'utf8')).toContain('import-attempt');
      expect(readFileSync(fixture.databasePath, 'utf8')).toBe('custom-database-before\n');
      expect(readFileSync(fixture.dataKeyPath, 'utf8')).toBe('custom-data-key-before\n');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('resolves Compose environment pass-through entries from the project environment', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const snapshotStore = path.join(installDir, 'fake-docker-snapshot');
    const fixture = writeCustomDockerInstall(installDir, { environmentSyntax: 'list' });
    const composePath = path.join(installDir, 'compose.yaml');
    const compose = readFileSync(composePath, 'utf8')
      .replace(
        '      - USAGE_DB_PATH=${CUSTOM_DB_PATH:-/data/usage.sqlite}',
        '      - USAGE_DB_PATH'
      )
      .replace(
        '      - CPA_MANAGER_DATA_KEY_PATH=${CUSTOM_DATA_KEY_PATH:-/data/data.key}',
        '      - CPA_MANAGER_DATA_KEY_PATH'
      );
    writeFileSync(composePath, compose);
    writeFileSync(
      path.join(installDir, '.env'),
      `${readFileSync(path.join(installDir, '.env'), 'utf8')}USAGE_DB_PATH=/data/custom.sqlite\nCPA_MANAGER_DATA_KEY_PATH=/data/custom.key\n`
    );

    try {
      writeFakeDocker(fakeBin);
      const environment = {
        ...process.env,
        CPAMP_OPERATION: 'upgrade',
        CPAMP_NON_INTERACTIVE: '1',
        CPAMP_CONFIRM: '1',
        CPAMP_LANG: 'en-US',
        CPAMP_INSTALL_DIR: installDir,
        FAKE_DOCKER_LOG: dockerLog,
        FAKE_DOCKER_DB_PATH: fixture.databasePath,
        FAKE_DOCKER_DATA_KEY_PATH: fixture.dataKeyPath,
        FAKE_DOCKER_EXPECTED_DB_PATH: '/data/custom.sqlite',
        FAKE_DOCKER_EXPECTED_DATA_KEY_PATH: '/data/custom.key',
        FAKE_DOCKER_MUTATE_ALL_DATA: '1',
        FAKE_DOCKER_SNAPSHOT_STORE: snapshotStore,
        PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
      };
      delete environment.USAGE_DB_PATH;
      delete environment.CPA_MANAGER_DATA_KEY_PATH;

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: environment,
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      const calls = readFileSync(dockerLog, 'utf8');
      expect(calls).toContain('--db-path /data/custom.sqlite');
      expect(calls).toContain('--data-key-path /data/custom.key');
      expect(readFileSync(fixture.databasePath, 'utf8')).toContain('import-attempt');
      expect(readFileSync(fixture.dataKeyPath, 'utf8')).toContain('import-attempt');
      expect(readFileSync(fixture.defaultDatabasePath, 'utf8')).toBe('default-database-sentinel\n');
      expect(readFileSync(fixture.defaultDataKeyPath, 'utf8')).toBe('default-data-key-sentinel\n');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('restores a custom Docker database, sidecars, and data key after CPA validation fails', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const snapshotStore = path.join(installDir, 'fake-docker-snapshot');
    const fixture = writeCustomDockerInstall(installDir, {
      environmentSyntax: 'list',
      volumeSyntax: 'long',
    });
    const beforeCompose = readFileSync(path.join(installDir, 'compose.yaml'), 'utf8');
    const beforeEnv = readFileSync(path.join(installDir, '.env'), 'utf8');

    try {
      writeFileSync(`${fixture.databasePath}-wal`, 'custom-wal-before\n');
      writeFileSync(`${fixture.databasePath}-shm`, 'custom-shm-before\n');
      writeFileSync(`${fixture.databasePath}-journal`, 'custom-journal-before\n');
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
          FAKE_DOCKER_DB_PATH: fixture.databasePath,
          FAKE_DOCKER_DATA_KEY_PATH: fixture.dataKeyPath,
          FAKE_DOCKER_EXPECTED_DB_PATH: '/data/custom.sqlite',
          FAKE_DOCKER_EXPECTED_DATA_KEY_PATH: '/data/custom.key',
          FAKE_DOCKER_MUTATE_ALL_DATA: '1',
          FAKE_DOCKER_SNAPSHOT_STORE: snapshotStore,
          FAKE_DOCKER_CPA_OK: '0',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('CPA connection validation failed');
      const calls = readFileSync(dockerLog, 'utf8');
      expect(calls).toContain('manager-data-snapshot restore');
      expect(calls).toContain('--db-path /data/custom.sqlite');
      expect(calls).toContain('--data-key-path /data/custom.key');
      expect(calls).not.toContain('--db-path /data/usage.sqlite');
      expect(calls).not.toContain('--data-key-path /data/data.key');
      expect(readFileSync(fixture.databasePath, 'utf8')).toBe('custom-database-before\n');
      expect(readFileSync(`${fixture.databasePath}-wal`, 'utf8')).toBe('custom-wal-before\n');
      expect(readFileSync(`${fixture.databasePath}-shm`, 'utf8')).toBe('custom-shm-before\n');
      expect(readFileSync(`${fixture.databasePath}-journal`, 'utf8')).toBe(
        'custom-journal-before\n'
      );
      expect(readFileSync(fixture.dataKeyPath, 'utf8')).toBe('custom-data-key-before\n');
      expect(readFileSync(fixture.defaultDatabasePath, 'utf8')).toBe('default-database-sentinel\n');
      expect(readFileSync(fixture.defaultDataKeyPath, 'utf8')).toBe('default-data-key-sentinel\n');
      expect(readFileSync(path.join(installDir, 'compose.yaml'), 'utf8')).toBe(beforeCompose);
      expect(readFileSync(path.join(installDir, '.env'), 'utf8')).toBe(beforeEnv);
      expect(readFileSync(fixture.keyPath, 'utf8')).toBe('cpa_custom_management_key\n');
      expect(existsSync(path.join(installDir, 'secrets/cpa-connection-import.pending'))).toBe(true);
      expect(
        readdirSync(installDir).some((name) =>
          name.startsWith('compose.yaml.cpa-key-migration.bak.')
        )
      ).toBe(true);
      expect(
        readdirSync(installDir).some((name) => name.startsWith('.env.cpa-key-migration.bak.'))
      ).toBe(true);
      expect(existsSync(snapshotStore)).toBe(false);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('fails closed before Docker import when a custom path is not mounted', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const fixture = writeCustomDockerInstall(installDir, { withVolume: false });
    const beforeCompose = readFileSync(path.join(installDir, 'compose.yaml'), 'utf8');
    const beforeEnv = readFileSync(path.join(installDir, '.env'), 'utf8');

    try {
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

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('not covered by a cpa-manager-plus volume mount');
      expect(readFileSync(path.join(installDir, 'compose.yaml'), 'utf8')).toBe(beforeCompose);
      expect(readFileSync(path.join(installDir, '.env'), 'utf8')).toBe(beforeEnv);
      expect(readFileSync(fixture.databasePath, 'utf8')).toBe('custom-database-before\n');
      expect(readFileSync(fixture.dataKeyPath, 'utf8')).toBe('custom-data-key-before\n');
      expect(readFileSync(fixture.defaultDatabasePath, 'utf8')).toBe('default-database-sentinel\n');
      expect(readFileSync(fixture.defaultDataKeyPath, 'utf8')).toBe('default-data-key-sentinel\n');
      expect(existsSync(fixture.keyPath)).toBe(true);
      expect(readFileSync(dockerLog, 'utf8')).not.toContain('store-cpa-connection');
      expect(readFileSync(dockerLog, 'utf8')).not.toContain('manager-data-snapshot');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('fails closed when a custom Docker path is not readable in the container', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const fixture = writeCustomDockerInstall(installDir);

    try {
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
          FAKE_DOCKER_PATH_ACCESS_OK: '0',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain(
        'not readable inside the cpa-manager-plus container'
      );
      expect(readFileSync(fixture.databasePath, 'utf8')).toBe('custom-database-before\n');
      expect(readFileSync(fixture.defaultDatabasePath, 'utf8')).toBe('default-database-sentinel\n');
      expect(readFileSync(fixture.defaultDataKeyPath, 'utf8')).toBe('default-data-key-sentinel\n');
      expect(existsSync(fixture.keyPath)).toBe(true);
      const calls = readFileSync(dockerLog, 'utf8');
      expect(calls).not.toContain('store-cpa-connection');
      expect(calls).not.toContain('manager-data-snapshot');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('fails closed when exact file mounts cannot provide a persistent snapshot directory', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const fixture = writeCustomDockerInstall(installDir, { volumeSyntax: 'exact-file' });

    try {
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

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('exact file mounts');
      expect(readFileSync(fixture.databasePath, 'utf8')).toBe('custom-database-before\n');
      expect(readFileSync(fixture.dataKeyPath, 'utf8')).toBe('custom-data-key-before\n');
      expect(readFileSync(fixture.keyPath, 'utf8')).toBe('cpa_custom_management_key\n');
      const calls = readFileSync(dockerLog, 'utf8');
      expect(calls).not.toContain('store-cpa-connection');
      expect(calls).not.toContain('manager-data-snapshot');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('fails closed when Docker Compose cannot resolve the existing configuration', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const fixture = writeCustomDockerInstall(installDir);

    try {
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
          FAKE_DOCKER_CONFIG_OK: '0',
          FAKE_DOCKER_LOG: dockerLog,
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain(
        'Unable to resolve the existing Docker Compose configuration'
      );
      expect(readFileSync(fixture.databasePath, 'utf8')).toBe('custom-database-before\n');
      expect(readFileSync(fixture.dataKeyPath, 'utf8')).toBe('custom-data-key-before\n');
      expect(existsSync(fixture.keyPath)).toBe(true);
      const calls = readFileSync(dockerLog, 'utf8');
      expect(calls).not.toContain('store-cpa-connection');
      expect(calls).not.toContain('manager-data-snapshot');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('fails closed on malformed .env interpolation instead of using default data paths', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const fixture = writeCustomDockerInstall(installDir);
    writeFileSync(
      path.join(installDir, '.env'),
      fixture.envContent.replace(
        'CUSTOM_DB_PATH=/data/custom.sqlite',
        'CUSTOM_DB_PATH="unterminated\n'
      )
    );

    try {
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

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('Unable to resolve USAGE_DB_PATH');
      expect(readFileSync(fixture.databasePath, 'utf8')).toBe('custom-database-before\n');
      expect(readFileSync(fixture.dataKeyPath, 'utf8')).toBe('custom-data-key-before\n');
      expect(existsSync(fixture.keyPath)).toBe(true);
      const calls = readFileSync(dockerLog, 'utf8');
      expect(calls).not.toContain('store-cpa-connection');
      expect(calls).not.toContain('manager-data-snapshot');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('fails closed on duplicate legacy Docker CPA key environment entries', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const envContent =
      'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\n' +
      'CPA_UPSTREAM_URL=http://host.docker.internal:8317\n' +
      'CPA_MANAGEMENT_KEY=cpa_duplicate_key\n';
    const composeContent = `services:
  cpa-manager-plus:
    image: \${CPAMP_IMAGE}
    environment:
      CPA_UPSTREAM_URL: "\${CPA_UPSTREAM_URL}"
      CPA_MANAGEMENT_KEY: "\${CPA_MANAGEMENT_KEY}"
      CPA_MANAGEMENT_KEY: "\${CPA_MANAGEMENT_KEY}"
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
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('duplicate CPA_MANAGEMENT_KEY environment entries');
      expect(readFileSync(path.join(installDir, '.env'), 'utf8')).toBe(envContent);
      expect(readFileSync(path.join(installDir, 'compose.yaml'), 'utf8')).toBe(composeContent);
      const calls = readFileSync(dockerLog, 'utf8');
      expect(calls).not.toContain('store-cpa-connection');
      expect(calls).not.toContain('manager-data-snapshot');
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
      ).toBe(false);
      expect(
        readdirSync(installDir).some((name) => name.startsWith('.env.cpa-key-migration.bak.'))
      ).toBe(false);
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

  it('migrates a legacy Docker .env CPA Management Key and removes rollback copies', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const marker = 'cpa_unique_plaintext_marker_585_a';
    const envContent =
      'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\nCPA_UPSTREAM_URL=http://host.docker.internal:8317\nCPA_MANAGEMENT_KEY=' +
      marker +
      '\n';
    const composeContent = `services:
  cpa-manager-plus:
    image: \${CPAMP_IMAGE}
    environment:
      - CPA_UPSTREAM_URL=\${CPA_UPSTREAM_URL}
      - CPA_MANAGEMENT_KEY=\${CPA_MANAGEMENT_KEY}
      - CUSTOM_VALUE=keep-me
`;

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(path.join(installDir, '.env'), envContent);
      writeFileSync(path.join(installDir, 'compose.yaml'), composeContent);
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      writeFakeDocker(fakeBin);

      const environment = {
        ...process.env,
        CPAMP_OPERATION: 'upgrade',
        CPAMP_NON_INTERACTIVE: '1',
        CPAMP_CONFIRM: '1',
        CPAMP_LANG: 'en-US',
        CPAMP_INSTALL_DIR: installDir,
        FAKE_DOCKER_LOG: dockerLog,
        FAKE_DOCKER_AUTH_OK: '1',
        PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
      };
      delete environment.CPA_UPSTREAM_URL;
      delete environment.CPA_MANAGEMENT_KEY;
      delete environment.CPA_MANAGEMENT_KEY_FILE;

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: environment,
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
      expect(migratedEnv).not.toContain(marker);
      expect(migratedCompose).not.toContain(marker);
      expect(
        readdirSync(installDir).some((name) =>
          name.startsWith('compose.yaml.cpa-key-migration.bak.')
        )
      ).toBe(false);
      expect(
        readdirSync(installDir).some((name) => name.startsWith('.env.cpa-key-migration.bak.'))
      ).toBe(false);
      expect(installerTextContents(installDir)).not.toContain(marker);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('migrates a legacy Docker inline Compose CPA Management Key and removes rollback copies', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const marker = 'cpa_unique_plaintext_marker_585_b';
    const envContent =
      'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\n' +
      'CPA_UPSTREAM_URL=http://host.docker.internal:8317\n';
    const composeContent = `services:
  cpa-manager-plus:
    image: \${CPAMP_IMAGE}
    environment:
      CPA_UPSTREAM_URL: "\${CPA_UPSTREAM_URL}"
      CPA_MANAGEMENT_KEY: "${marker}"
      CUSTOM_VALUE: "keep-me"
`;

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(path.join(installDir, '.env'), envContent);
      writeFileSync(path.join(installDir, 'compose.yaml'), composeContent);
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      writeFakeDocker(fakeBin);

      const environment = {
        ...process.env,
        CPAMP_OPERATION: 'upgrade',
        CPAMP_NON_INTERACTIVE: '1',
        CPAMP_CONFIRM: '1',
        CPAMP_LANG: 'en-US',
        CPAMP_INSTALL_DIR: installDir,
        FAKE_DOCKER_LOG: dockerLog,
        FAKE_DOCKER_AUTH_OK: '1',
        PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
      };
      delete environment.CPA_UPSTREAM_URL;
      delete environment.CPA_MANAGEMENT_KEY;
      delete environment.CPA_MANAGEMENT_KEY_FILE;

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: environment,
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      const migratedEnv = readFileSync(path.join(installDir, '.env'), 'utf8');
      const migratedCompose = readFileSync(path.join(installDir, 'compose.yaml'), 'utf8');
      expect(migratedEnv).not.toContain(marker);
      expect(migratedCompose).not.toContain(marker);
      expect(migratedCompose).toContain('CUSTOM_VALUE: "keep-me"');
      expect(existsSync(path.join(installDir, 'secrets/cpa-management-key'))).toBe(false);
      expect(
        readdirSync(installDir).some((name) =>
          name.startsWith('compose.yaml.cpa-key-migration.bak.')
        )
      ).toBe(false);
      expect(
        readdirSync(installDir).some((name) => name.startsWith('.env.cpa-key-migration.bak.'))
      ).toBe(false);
      expect(installerTextContents(installDir)).not.toContain(marker);
      expect(readFileSync(dockerLog, 'utf8')).toContain('store-cpa-connection');
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

  it('preserves an external CPA key at the canonical managed path across reruns', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const externalKeyPath = path.join(installDir, 'secrets/cpa-management-key');
    const composeContent = `services:
  cpa-manager-plus:
    image: \${CPAMP_IMAGE}
    environment:
      CPA_UPSTREAM_URL: "\${CPA_UPSTREAM_URL}"
      CPA_MANAGEMENT_KEY_FILE: "\${CPA_MANAGEMENT_KEY_FILE}"
`;

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(
        path.join(installDir, '.env'),
        'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\n' +
          'CPA_UPSTREAM_URL=http://host.docker.internal:8317\n' +
          'CPA_MANAGEMENT_KEY_FILE=./secrets/cpa-management-key\n'
      );
      writeFileSync(path.join(installDir, 'compose.yaml'), composeContent);
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      writeFileSync(externalKeyPath, 'cpa_external_canonical_key\n');
      chmodSync(externalKeyPath, 0o640);
      writeFakeDocker(fakeBin);

      const commonEnv = {
        ...process.env,
        CPA_UPSTREAM_URL: 'http://host.docker.internal:8317',
        CPA_MANAGEMENT_KEY: '',
        CPA_MANAGEMENT_KEY_FILE: externalKeyPath,
        CPAMP_OPERATION: 'upgrade',
        CPAMP_NON_INTERACTIVE: '1',
        CPAMP_CONFIRM: '1',
        CPAMP_LANG: 'en-US',
        CPAMP_INSTALL_DIR: installDir,
        FAKE_DOCKER_LOG: dockerLog,
        PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
      };
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: commonEnv,
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      expect(readFileSync(externalKeyPath, 'utf8')).toBe('cpa_external_canonical_key\n');
      expect(statSync(externalKeyPath).mode & 0o777).toBe(0o640);
      expect(
        readFileSync(path.join(installDir, 'secrets/.cpa-management-key.external'), 'utf8')
      ).toBe('EXTERNAL=1\n');
      expect(result.stdout).not.toContain(`rm -f "${externalKeyPath}"`);
      expect(readFileSync(path.join(installDir, 'compose.yaml'), 'utf8')).not.toContain(
        'CPA_MANAGEMENT_KEY_FILE'
      );

      // Once the runtime reference is gone, the marker must keep a later
      // stored-mode rerun from classifying the external file as leftover
      // installer state and deleting it.
      const rerun = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: commonEnv,
        encoding: 'utf8',
      });
      expect(rerun.status).toBe(0);
      expect(readFileSync(externalKeyPath, 'utf8')).toBe('cpa_external_canonical_key\n');
      expect(statSync(externalKeyPath).mode & 0o777).toBe(0o640);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('never deletes an external canonical CPA key after a failed import retry', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const externalKeyPath = path.join(installDir, 'secrets/cpa-management-key');
    const composeContent = `services:
  cpa-manager-plus:
    image: \${CPAMP_IMAGE}
    environment:
      CPA_UPSTREAM_URL: "\${CPA_UPSTREAM_URL}"
      CPA_MANAGEMENT_KEY_FILE: "\${CPA_MANAGEMENT_KEY_FILE}"
`;

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(
        path.join(installDir, '.env'),
        'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\n' +
          'CPA_UPSTREAM_URL=http://host.docker.internal:8317\n' +
          'CPA_MANAGEMENT_KEY_FILE=./secrets/cpa-management-key\n'
      );
      writeFileSync(path.join(installDir, 'compose.yaml'), composeContent);
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      writeFileSync(externalKeyPath, 'cpa_external_canonical_key\n');
      chmodSync(externalKeyPath, 0o640);
      writeFakeDocker(fakeBin);

      const commonEnv = {
        ...process.env,
        CPA_UPSTREAM_URL: 'http://host.docker.internal:8317',
        CPA_MANAGEMENT_KEY: '',
        CPA_MANAGEMENT_KEY_FILE: externalKeyPath,
        CPAMP_OPERATION: 'upgrade',
        CPAMP_NON_INTERACTIVE: '1',
        CPAMP_CONFIRM: '1',
        CPAMP_LANG: 'en-US',
        CPAMP_INSTALL_DIR: installDir,
        FAKE_DOCKER_LOG: dockerLog,
        PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
      };
      const failed = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: { ...commonEnv, FAKE_DOCKER_CPA_OK: '0' },
        encoding: 'utf8',
      });

      expect(failed.status).toBe(1);
      expect(combinedOutput(failed)).toContain('CPA connection validation failed');
      expect(readFileSync(externalKeyPath, 'utf8')).toBe('cpa_external_canonical_key\n');
      expect(statSync(externalKeyPath).mode & 0o777).toBe(0o640);
      expect(existsSync(path.join(installDir, 'secrets/cpa-connection-import.pending'))).toBe(
        false
      );

      const succeeded = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: { ...commonEnv, FAKE_DOCKER_CPA_OK: '1' },
        encoding: 'utf8',
      });

      expect(succeeded.status).toBe(0);
      expect(readFileSync(externalKeyPath, 'utf8')).toBe('cpa_external_canonical_key\n');
      expect(statSync(externalKeyPath).mode & 0o777).toBe(0o640);
      expect(
        readFileSync(path.join(installDir, 'secrets/.cpa-management-key.external'), 'utf8')
      ).toBe('EXTERNAL=1\n');
      expect(combinedOutput(succeeded)).not.toContain(`rm -f "${externalKeyPath}"`);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('treats a managed-path CPA key symlink as external during a Docker migration', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const importedKeyLog = path.join(
      os.tmpdir(),
      `cpamp-imported-key-${process.pid}-${Date.now()}`
    );
    const externalDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-external-secret-'));
    const externalKeyPath = path.join(externalDir, 'external-cpa-key');
    const managedKeyPath = path.join(installDir, 'secrets/cpa-management-key');
    const envContent =
      'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\n' +
      'CPA_UPSTREAM_URL=http://host.docker.internal:8317\n' +
      'CPA_MANAGEMENT_KEY_FILE=./secrets/cpa-management-key\n';
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
      writeFileSync(externalKeyPath, 'EXTERNAL_SECRET\n');
      chmodSync(externalKeyPath, 0o640);
      symlinkSync(externalKeyPath, managedKeyPath);
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
          FAKE_DOCKER_IMPORTED_KEY_LOG: importedKeyLog,
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      // The external target keeps its exact content, mode, and existence.
      expect(existsSync(externalKeyPath)).toBe(true);
      expect(readFileSync(externalKeyPath, 'utf8')).toBe('EXTERNAL_SECRET\n');
      expect(statSync(externalKeyPath).mode & 0o777).toBe(0o640);
      // The symlink itself survives the migration and finalization.
      expect(existsSync(managedKeyPath)).toBe(true);
      expect(lstatSync(managedKeyPath).isSymbolicLink()).toBe(true);
      // The connection import still completed using the symlinked secret.
      expect(readFileSync(dockerLog, 'utf8')).toContain('store-cpa-connection');
      expect(readFileSync(importedKeyLog, 'utf8')).toBe('EXTERNAL_SECRET\n');
      expect(readFileSync(path.join(installDir, 'compose.yaml'), 'utf8')).not.toContain(
        'CPA_MANAGEMENT_KEY_FILE'
      );
      expect(combinedOutput(result)).not.toContain(`rm -f "${managedKeyPath}"`);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(externalDir, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
      rmSync(importedKeyLog, { force: true });
    }
  });

  it('keeps a managed-path CPA key symlink intact when the Docker migration fails', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const externalDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-external-secret-'));
    const externalKeyPath = path.join(externalDir, 'external-cpa-key');
    const managedKeyPath = path.join(installDir, 'secrets/cpa-management-key');
    const envContent =
      'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\n' +
      'CPA_UPSTREAM_URL=http://host.docker.internal:8317\n' +
      'CPA_MANAGEMENT_KEY_FILE=./secrets/cpa-management-key\n';
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
      writeFileSync(externalKeyPath, 'EXTERNAL_SECRET\n');
      chmodSync(externalKeyPath, 0o640);
      symlinkSync(externalKeyPath, managedKeyPath);
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
          FAKE_DOCKER_IMPORT_OK: '0',
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(existsSync(externalKeyPath)).toBe(true);
      expect(readFileSync(externalKeyPath, 'utf8')).toBe('EXTERNAL_SECRET\n');
      expect(statSync(externalKeyPath).mode & 0o777).toBe(0o640);
      expect(existsSync(managedKeyPath)).toBe(true);
      expect(lstatSync(managedKeyPath).isSymbolicLink()).toBe(true);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(externalDir, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('ignores a managed-path CPA key symlink when finalizing a Docker rerun', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const externalDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-external-secret-'));
    const externalKeyPath = path.join(externalDir, 'external-cpa-key');
    const managedKeyPath = path.join(installDir, 'secrets/cpa-management-key');

    try {
      // Post-failed-install state: the stored connection is already verified
      // while a user-managed symlink still occupies the managed key path.
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
      writeFileSync(externalKeyPath, 'EXTERNAL_SECRET\n');
      chmodSync(externalKeyPath, 0o640);
      symlinkSync(externalKeyPath, managedKeyPath);
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
      expect(readFileSync(dockerLog, 'utf8')).not.toContain('store-cpa-connection');
      expect(existsSync(externalKeyPath)).toBe(true);
      expect(readFileSync(externalKeyPath, 'utf8')).toBe('EXTERNAL_SECRET\n');
      expect(statSync(externalKeyPath).mode & 0o777).toBe(0o640);
      expect(existsSync(managedKeyPath)).toBe(true);
      expect(lstatSync(managedKeyPath).isSymbolicLink()).toBe(true);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(externalDir, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('sweeps stale CPA key import copies on a successful Docker rerun', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const staleCopyPath = path.join(installDir, 'secrets/cpa-management-key.import.99999');

    try {
      // Stored-mode rerun state with a plaintext import copy left behind by a
      // failed inline import from an earlier installer run.
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
      writeFileSync(staleCopyPath, 'stale_inline_key\n');
      chmodSync(staleCopyPath, 0o600);
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
      expect(readFileSync(dockerLog, 'utf8')).not.toContain('store-cpa-connection');
      // The stored connection is re-verified before the stale copy is removed.
      expect(readFileSync(dockerLog, 'utf8')).toContain('/v0/management/cpa-connection/validate');
      expect(existsSync(staleCopyPath)).toBe(false);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('keeps stale CPA key import copies when the Docker rerun proxy validation fails', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const staleCopyPath = path.join(installDir, 'secrets/cpa-management-key.import.99999');

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
      writeFileSync(staleCopyPath, 'stale_inline_key\n');
      chmodSync(staleCopyPath, 0o600);
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
      expect(existsSync(staleCopyPath)).toBe(true);
      expect(readFileSync(staleCopyPath, 'utf8')).toBe('stale_inline_key\n');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
    }
  });

  it('materializes an inline Docker CPA key next to a dangling managed-path symlink', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const importedKeyLog = path.join(
      os.tmpdir(),
      `cpamp-imported-key-${process.pid}-${Date.now()}`
    );
    const externalDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-external-secret-'));
    const danglingTargetPath = path.join(externalDir, 'missing-cpa-key');
    const managedKeyPath = path.join(installDir, 'secrets/cpa-management-key');
    const envContent =
      'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\n' +
      'CPA_UPSTREAM_URL=http://host.docker.internal:8317\n' +
      'CPA_MANAGEMENT_KEY=cpa_inline_key\n';
    const composeContent = `services:
  cpa-manager-plus:
    image: \${CPAMP_IMAGE}
    environment:
      CPA_UPSTREAM_URL: "\${CPA_UPSTREAM_URL}"
      CPA_MANAGEMENT_KEY: "\${CPA_MANAGEMENT_KEY}"
    `;

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      writeFileSync(path.join(installDir, '.env'), envContent);
      writeFileSync(path.join(installDir, 'compose.yaml'), composeContent);
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      symlinkSync(danglingTargetPath, managedKeyPath);
      // A plaintext import copy left behind by an earlier failed inline import
      // must be swept by this run together with the fresh copy.
      const staleCopyPath = path.join(installDir, 'secrets/cpa-management-key.import.424242');
      writeFileSync(staleCopyPath, 'stale_inline_key\n');
      chmodSync(staleCopyPath, 0o600);
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
          FAKE_DOCKER_IMPORTED_KEY_LOG: importedKeyLog,
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      // The import used the installer-created copy, not the symlink target.
      expect(readFileSync(dockerLog, 'utf8')).toContain('store-cpa-connection');
      expect(readFileSync(importedKeyLog, 'utf8')).toBe('cpa_inline_key\n');
      // The dangling symlink and its (still missing) external target survive.
      expect(lstatSync(managedKeyPath).isSymbolicLink()).toBe(true);
      expect(existsSync(danglingTargetPath)).toBe(false);
      // No import copies are left behind after successful finalization.
      expect(
        readdirSync(path.join(installDir, 'secrets')).filter((name) =>
          name.startsWith('cpa-management-key.import.')
        )
      ).toEqual([]);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(externalDir, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
      rmSync(importedKeyLog, { force: true });
    }
  });

  it('uses process CPA inputs instead of stale env and installer-managed secret values', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const externalDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-external-secret-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const importedKeyLog = path.join(
      os.tmpdir(),
      `cpamp-imported-key-${process.pid}-${Date.now()}`
    );
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
      expect(readFileSync(dockerLog, 'utf8')).toContain(
        '--cpa-base-url http://process.example:8317'
      );
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
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const importedKeyLog = path.join(
      os.tmpdir(),
      `cpamp-imported-key-${process.pid}-${Date.now()}`
    );
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
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
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
      expect(readFileSync(dockerLog, 'utf8')).toContain('/v0/management/cpa-connection/validate');
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
      expect(combinedOutput(result)).toContain(
        'Unable to resolve CPA_MANAGEMENT_KEY from the existing Docker Compose configuration.'
      );
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
      expect(combinedOutput(result)).toContain(
        'previous runtime config and temporary secret were preserved'
      );
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
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
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
      expect(calls).toContain('/v0/management/cpa-connection/validate');
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

  it('treats a managed-path CPA key symlink as external during a native upgrade', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const release = writeFakeNativeRelease();
    const legacy = writeLegacyNativeInstall(installDir);
    const commandLog = path.join(installDir, 'native-command.log');
    const externalDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-external-secret-'));
    const externalKeyPath = path.join(externalDir, 'external-cpa-key');

    // Replace the fixture's installer-managed key with a user-managed symlink
    // to an external secret with intentionally non-installer permissions.
    rmSync(legacy.cpaKeyPath);
    writeFileSync(externalKeyPath, 'EXTERNAL_SECRET\n');
    chmodSync(externalKeyPath, 0o640);
    symlinkSync(externalKeyPath, legacy.cpaKeyPath);

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
      // The external target keeps its exact content, mode, and existence.
      expect(existsSync(externalKeyPath)).toBe(true);
      expect(readFileSync(externalKeyPath, 'utf8')).toBe('EXTERNAL_SECRET\n');
      expect(statSync(externalKeyPath).mode & 0o777).toBe(0o640);
      // The symlink itself survives migration and finalization.
      expect(existsSync(legacy.cpaKeyPath)).toBe(true);
      expect(lstatSync(legacy.cpaKeyPath).isSymbolicLink()).toBe(true);
      // The connection import still completed, reading through the symlink.
      const commands = readFileSync(commandLog, 'utf8');
      const canonicalKeyPath = path.join(
        realpathSync(path.dirname(legacy.cpaKeyPath)),
        path.basename(legacy.cpaKeyPath)
      );
      expect(commands).toContain('store-cpa-connection');
      expect(commands).toContain(`--management-key-file ${canonicalKeyPath}`);
      expect(readFileSync(legacy.dbPath, 'utf8')).toBe('existing-usage-data\nimport-attempt\n');
      const upgradedConfig = JSON.parse(
        readFileSync(path.join(installDir, 'runtime', release.packageName, 'config.json'), 'utf8')
      );
      expect(upgradedConfig.cpaUpstreamUrl).toBeUndefined();
      expect(upgradedConfig.managementKeyFile).toBeUndefined();
    } finally {
      stopNativeFixtureProcess(installDir);
      rmSync(installDir, { recursive: true, force: true });
      rmSync(release.fakeBin, { recursive: true, force: true });
      rmSync(release.fixtureDir, { recursive: true, force: true });
      rmSync(externalDir, { recursive: true, force: true });
    }
  });

  it('keeps a managed-path CPA key symlink intact when the native migration fails', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const release = writeFakeNativeRelease();
    const legacy = writeLegacyNativeInstall(installDir);
    const commandLog = path.join(installDir, 'native-command.log');
    const externalDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-external-secret-'));
    const externalKeyPath = path.join(externalDir, 'external-cpa-key');

    rmSync(legacy.cpaKeyPath);
    writeFileSync(externalKeyPath, 'EXTERNAL_SECRET\n');
    chmodSync(externalKeyPath, 0o640);
    symlinkSync(externalKeyPath, legacy.cpaKeyPath);

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
          FAKE_NATIVE_IMPORT_OK: '0',
          PATH: `${release.fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(existsSync(externalKeyPath)).toBe(true);
      expect(readFileSync(externalKeyPath, 'utf8')).toBe('EXTERNAL_SECRET\n');
      expect(statSync(externalKeyPath).mode & 0o777).toBe(0o640);
      expect(existsSync(legacy.cpaKeyPath)).toBe(true);
      expect(lstatSync(legacy.cpaKeyPath).isSymbolicLink()).toBe(true);
      // Rollback restored the pre-migration database file set.
      expect(readFileSync(legacy.dbPath, 'utf8')).toBe('existing-usage-data\n');
    } finally {
      stopNativeFixtureProcess(installDir);
      rmSync(installDir, { recursive: true, force: true });
      rmSync(release.fakeBin, { recursive: true, force: true });
      rmSync(release.fixtureDir, { recursive: true, force: true });
      rmSync(externalDir, { recursive: true, force: true });
    }
  });

  it('does not roll back a committed Docker migration when snapshot cleanup fails', () => {
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

  it('does not roll back a committed Docker migration when runtime backup cleanup fails', () => {
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
    const marker = 'cpa_unique_plaintext_marker_585_cleanup';
    const envContent =
      'COMPOSE_PROJECT_NAME=cpamp\nCPAMP_IMAGE=example/cpamp:v1\nCPAMP_PORT=18317\n' +
      'CPA_UPSTREAM_URL=http://host.docker.internal:8317\nCPA_MANAGEMENT_KEY=' +
      marker +
      '\n';
    const composeContent = `services:
  cpa-manager-plus:
    image: \${CPAMP_IMAGE}
    environment:
      CPA_UPSTREAM_URL: "\${CPA_UPSTREAM_URL}"
      CPA_MANAGEMENT_KEY: "\${CPA_MANAGEMENT_KEY}"
`;

    try {
      mkdirSync(path.join(installDir, 'secrets'), { recursive: true });
      mkdirSync(dockerDataDir, { recursive: true });
      writeFileSync(path.join(installDir, '.env'), envContent);
      writeFileSync(path.join(installDir, 'compose.yaml'), composeContent);
      writeFileSync(path.join(installDir, 'secrets/cpamp-admin-key'), 'cpamp_existing_admin_key\n');
      writeFileSync(dbPath, 'database-before\n');
      writeFileSync(dataKeyPath, 'data-key-before\n');
      writeFakeDocker(fakeBin);
      writeFakeRmThatFailsForMigrationBackups(fakeBin);

      const environment = {
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
        PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
      };
      delete environment.CPA_UPSTREAM_URL;
      delete environment.CPA_MANAGEMENT_KEY;
      delete environment.CPA_MANAGEMENT_KEY_FILE;

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: environment,
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      expect(combinedOutput(result)).toContain(
        'legacy CPA runtime rollback backup could not be removed'
      );
      expect(combinedOutput(result)).toContain('legacy CPA secret');
      expect(combinedOutput(result)).toContain('.cpa-key-migration.bak.');
      const calls = readFileSync(dockerLog, 'utf8');
      expect(calls).not.toContain('manager-data-snapshot restore');
      expect(calls.match(/compose stop/g)).toHaveLength(1);
      expect(readFileSync(path.join(installDir, '.env'), 'utf8')).not.toContain(marker);
      expect(readFileSync(path.join(installDir, 'compose.yaml'), 'utf8')).not.toContain(marker);
      expect(readFileSync(dbPath, 'utf8')).toContain('import-attempt');
      expect(existsSync(snapshotStore)).toBe(false);
      expect(existsSync(path.join(installDir, 'secrets/cpa-management-key'))).toBe(false);
      expect(
        readdirSync(installDir).some((name) =>
          name.startsWith('compose.yaml.cpa-key-migration.bak.')
        )
      ).toBe(true);
      expect(
        readdirSync(installDir).some((name) => name.startsWith('.env.cpa-key-migration.bak.'))
      ).toBe(true);
      expect(installerTextContents(installDir)).toContain(marker);
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
      expect(
        readdirSync(installDir).some((name) =>
          name.startsWith('compose.yaml.cpa-key-migration.bak.')
        )
      ).toBe(true);
      expect(
        readdirSync(installDir).some((name) => name.startsWith('.env.cpa-key-migration.bak.'))
      ).toBe(true);
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
      expect(readFileSync(legacy.dbPath, 'utf8')).toBe('existing-usage-data\nimport-attempt\n');
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

  it('uses the native run and systemd resolved custom database and data-key paths', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const release = writeFakeNativeRelease();
    const customDataDir = path.join(installDir, 'custom-data');
    const customDbPath = path.join(customDataDir, 'custom.sqlite');
    const customDataKeyPath = path.join(customDataDir, 'custom.key');
    const commandLog = path.join(installDir, 'native-command.log');
    const legacy = writeLegacyNativeInstall(installDir, {
      dbPath: customDbPath,
      dataKeyPath: customDataKeyPath,
      configDataDir: customDataDir,
      configDataKeyPath: customDataKeyPath,
      runEnvironment:
        `export USAGE_DATA_DIR=${customDataDir}\n` +
        `export USAGE_DB_PATH=${customDbPath}\n` +
        `export CPA_MANAGER_DATA_KEY_PATH=${customDataKeyPath}\n`,
      serviceEnvironment:
        `Environment="USAGE_DATA_DIR=${customDataDir}"\n` +
        `Environment="USAGE_DB_PATH=${customDbPath}"\n` +
        `Environment="CPA_MANAGER_DATA_KEY_PATH=${customDataKeyPath}"\n`,
    });

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
          FAKE_NATIVE_DB_PATH: customDbPath,
          FAKE_NATIVE_DATA_KEY_PATH: customDataKeyPath,
          FAKE_NATIVE_MUTATE_ALL_DATA: '1',
          PATH: `${release.fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      const commands = readFileSync(commandLog, 'utf8');
      expect(commands).toContain(`--db-path ${realpathSync(customDbPath)}`);
      expect(commands).toContain(`--data-key-path ${realpathSync(customDataKeyPath)}`);
      expect(commands).not.toContain('--db-path /data/usage.sqlite');
      expect(commands).not.toContain('--data-key-path /data/data.key');
      expect(readFileSync(customDbPath, 'utf8')).toContain('import-attempt');
      expect(readFileSync(customDataKeyPath, 'utf8')).toContain('import-attempt');
      expect(existsSync(path.join(installDir, 'data', 'usage.sqlite'))).toBe(false);
      expect(existsSync(path.join(installDir, 'data', 'data.key'))).toBe(false);
      const runScript = readFileSync(legacy.runPath, 'utf8');
      expect(runScript).toContain(`export USAGE_DB_PATH=${realpathSync(customDbPath)}`);
      expect(runScript).toContain(
        `export CPA_MANAGER_DATA_KEY_PATH=${realpathSync(customDataKeyPath)}`
      );
      const service = readFileSync(path.join(installDir, 'cpa-manager-plus.service'), 'utf8');
      expect(service).toContain('Environment="USAGE_DB_PATH=');
      expect(service).toContain(`${path.basename(customDbPath)}"`);
      expect(service).toContain('Environment="CPA_MANAGER_DATA_KEY_PATH=');
      expect(service).toContain(`${path.basename(customDataKeyPath)}"`);
    } finally {
      stopNativeFixtureProcess(installDir);
      rmSync(installDir, { recursive: true, force: true });
      rmSync(release.fakeBin, { recursive: true, force: true });
      rmSync(release.fixtureDir, { recursive: true, force: true });
    }
  });

  it('fails closed before native upgrade when the configured data key is missing', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const legacy = writeLegacyNativeInstall(installDir);
    rmSync(legacy.dataKeyPath);
    const beforeRun = readFileSync(legacy.runPath, 'utf8');
    const beforeDatabase = readFileSync(legacy.dbPath, 'utf8');

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
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('Manager data key is missing');
      expect(readFileSync(legacy.runPath, 'utf8')).toBe(beforeRun);
      expect(readFileSync(legacy.dbPath, 'utf8')).toBe(beforeDatabase);
      expect(existsSync(legacy.dataKeyPath)).toBe(false);
      expect(
        existsSync(path.join(installDir, 'runtime', 'cpa-manager-plus_vnext_linux_amd64'))
      ).toBe(false);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
    }
  });

  it('rejects conflicting native run and systemd data paths before upgrade', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const legacy = writeLegacyNativeInstall(installDir, {
      runEnvironment: `export USAGE_DB_PATH=${path.join(installDir, 'other.sqlite')}\n`,
    });
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
      expect(combinedOutput(result)).toContain('run.sh and systemd service resolve different');
      expect(readFileSync(legacy.runPath, 'utf8')).toBe(beforeRun);
      expect(readFileSync(legacy.dbPath, 'utf8')).toBe('existing-usage-data\n');
    } finally {
      rmSync(installDir, { recursive: true, force: true });
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
      expect(readFileSync(legacy.dbPath, 'utf8')).toBe('existing-usage-data\nimport-attempt\n');
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
          path.join(installDir, 'runtime', `cpa-manager-plus_vnext_${nativePlatform}_${nativeArch}`)
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
      expect(combinedOutput(result)).toContain('dataKeyPath as a supported string value');
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
      expect(combinedOutput(result)).toContain(
        'Failed to stop the existing native CPA Manager Plus process'
      );
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
    const beforeService = readFileSync(path.join(installDir, 'cpa-manager-plus.service'), 'utf8');
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

  it('restores the complete native data file-set after a partially mutating import failure', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const release = writeFakeNativeRelease();
    const legacy = writeLegacyNativeInstall(installDir);
    const beforeRun = readFileSync(legacy.runPath, 'utf8');
    const beforeService = readFileSync(path.join(installDir, 'cpa-manager-plus.service'), 'utf8');
    const sidecars = [legacy.dbPath + '-wal', legacy.dbPath + '-shm', legacy.dbPath + '-journal'];
    const beforeFiles = [
      [legacy.dbPath, 'existing-usage-data\n'],
      [legacy.dataKeyPath, 'existing-data-key\n'],
      ...sidecars.map((file, index) => [file, 'existing-sidecar-' + index + '\n']),
    ];
    for (const [file, content] of beforeFiles) writeFileSync(file, content);

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
          FAKE_NATIVE_COMMAND_LOG: path.join(installDir, 'native-command.log'),
          FAKE_NATIVE_DB_PATH: legacy.dbPath,
          FAKE_NATIVE_DATA_KEY_PATH: legacy.dataKeyPath,
          FAKE_NATIVE_MUTATE_ALL_DATA: '1',
          FAKE_NATIVE_IMPORT_OK: '0',
          PATH: release.fakeBin + path.delimiter + (process.env.PATH || ''),
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('CPA connection import failed');
      for (const [file, content] of beforeFiles) {
        expect(readFileSync(file, 'utf8')).toBe(content);
      }
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

  it('restores absent native sidecars and an absent data key after rollback', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const release = writeFakeNativeRelease();
    const legacy = writeLegacyNativeInstall(installDir);
    rmSync(legacy.dataKeyPath);
    const sidecars = [legacy.dbPath + '-wal', legacy.dbPath + '-shm', legacy.dbPath + '-journal'];
    for (const file of sidecars) rmSync(file, { force: true });

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
          FAKE_NATIVE_COMMAND_LOG: path.join(installDir, 'native-command.log'),
          FAKE_NATIVE_DB_PATH: legacy.dbPath,
          FAKE_NATIVE_DATA_KEY_PATH: legacy.dataKeyPath,
          FAKE_NATIVE_MUTATE_ALL_DATA: '1',
          FAKE_NATIVE_IMPORT_OK: '0',
          PATH: release.fakeBin + path.delimiter + (process.env.PATH || ''),
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(readFileSync(legacy.dbPath, 'utf8')).toBe('existing-usage-data\n');
      expect(existsSync(legacy.dataKeyPath)).toBe(false);
      for (const file of sidecars) expect(existsSync(file)).toBe(false);
    } finally {
      stopNativeFixtureProcess(installDir);
      rmSync(installDir, { recursive: true, force: true });
      rmSync(release.fakeBin, { recursive: true, force: true });
      rmSync(release.fixtureDir, { recursive: true, force: true });
    }
  });

  it('does not restart the previous native runtime when snapshot restore fails', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const release = writeFakeNativeRelease();
    const legacy = writeLegacyNativeInstall(installDir);
    const startMarker = path.join(installDir, 'legacy-starts.log');
    const oldProcess = spawn(legacy.runPath, {
      cwd: installDir,
      env: { ...process.env, FAKE_LEGACY_NATIVE_START_MARKER: startMarker },
      stdio: 'ignore',
    });
    const oldPID = oldProcess.pid;
    const pidPath = path.join(installDir, 'cpa-manager-plus.pid');

    try {
      // Wait until the legacy process has recorded its start before killing
      // it; process startup latency varies across machines.
      const markerDeadline = Date.now() + 5000;
      while (!existsSync(startMarker) && Date.now() < markerDeadline) {
        spawnSync('bash', ['-c', 'sleep 0.05'], { encoding: 'utf8' });
      }
      expect(readFileSync(startMarker, 'utf8')).toBe('start\n');
      process.kill(oldPID, 'SIGKILL');
      spawnSync('bash', ['-c', 'sleep 0.1'], { encoding: 'utf8' });
      expect(readFileSync(startMarker, 'utf8')).toBe('start\n');
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
          FAKE_NATIVE_COMMAND_LOG: path.join(installDir, 'native-command.log'),
          FAKE_NATIVE_DB_PATH: legacy.dbPath,
          FAKE_NATIVE_DATA_KEY_PATH: legacy.dataKeyPath,
          FAKE_NATIVE_MUTATE_ALL_DATA: '1',
          FAKE_NATIVE_SNAPSHOT_RESTORE_FAIL_AFTER_DATABASE: '1',
          FAKE_NATIVE_CPA_OK: '0',
          PATH: release.fakeBin + path.delimiter + (process.env.PATH || ''),
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain(
        'Automatic native upgrade rollback did not fully succeed'
      );
      expect(existsSync(pidPath)).toBe(false);
      expect(readFileSync(startMarker, 'utf8')).toBe('start\n');
    } finally {
      try {
        process.kill(oldPID, 'SIGKILL');
      } catch {
        // The old process was stopped by the installer or already exited.
      }
      stopNativeFixtureProcess(installDir);
      rmSync(installDir, { recursive: true, force: true });
      rmSync(release.fakeBin, { recursive: true, force: true });
      rmSync(release.fixtureDir, { recursive: true, force: true });
    }
  });

  it('stops a new native process when PID publication fails before rollback', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const release = writeFakeNativeRelease();
    const legacy = writeLegacyNativeInstall(installDir);
    mkdirSync(path.join(installDir, 'cpa-manager-plus.pid'));

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
          FAKE_NATIVE_COMMAND_LOG: path.join(installDir, 'native-command.log'),
          FAKE_NATIVE_DB_PATH: legacy.dbPath,
          FAKE_NATIVE_DATA_KEY_PATH: legacy.dataKeyPath,
          FAKE_NATIVE_CPA_OK: '1',
          PATH: release.fakeBin + path.delimiter + (process.env.PATH || ''),
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      spawnSync('sleep', ['0.2'], { encoding: 'utf8' });
      const processTable = spawnSync('ps', ['-axo', 'pid=,command='], {
        encoding: 'utf8',
      });
      const newRuntimePrefix = path.join(installDir, 'runtime', 'cpa-manager-plus_vnext');
      const leakedPids = (processTable.stdout || '')
        .split('\n')
        .filter((line) => line.includes(newRuntimePrefix))
        .map((line) => Number(line.trim().split(/\s+/, 1)[0]))
        .filter((pid) => Number.isInteger(pid) && pid > 0);
      for (const pid of leakedPids) {
        try {
          process.kill(pid, 'SIGKILL');
        } catch {
          // The process may have exited between ps and cleanup.
        }
      }
      expect(leakedPids).toEqual([]);
      expect(readFileSync(legacy.dbPath, 'utf8')).toBe('existing-usage-data\n');
      expect(readFileSync(legacy.runPath, 'utf8')).toContain(legacy.binaryDir);
    } finally {
      stopNativeFixtureProcess(installDir);
      rmSync(installDir, { recursive: true, force: true });
      rmSync(release.fakeBin, { recursive: true, force: true });
      rmSync(release.fixtureDir, { recursive: true, force: true });
    }
  });

  it('retains a native snapshot when post-commit cleanup fails', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const release = writeFakeNativeRelease();
    const legacy = writeLegacyNativeInstall(installDir);

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
          FAKE_NATIVE_COMMAND_LOG: path.join(installDir, 'native-command.log'),
          FAKE_NATIVE_DB_PATH: legacy.dbPath,
          FAKE_NATIVE_DATA_KEY_PATH: legacy.dataKeyPath,
          FAKE_NATIVE_SNAPSHOT_DELETE_OK: '0',
          PATH: release.fakeBin + path.delimiter + (process.env.PATH || ''),
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      expect(combinedOutput(result)).toContain('cleaning the Manager data snapshot failed');
      expect(readFileSync(legacy.dbPath, 'utf8')).toBe('existing-usage-data\nimport-attempt\n');
      expect(existsSync(legacy.cpaKeyPath)).toBe(false);
      const backupEntries = readdirSync(path.join(installDir, 'backups'));
      expect(backupEntries.length).toBe(1);
      expect(
        existsSync(path.join(installDir, 'backups', backupEntries[0], 'manager-data-snapshot'))
      ).toBe(true);
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
    const pendingPath = path.join(installDir, 'secrets/cpa-connection-import.pending');
    const beforeRun = readFileSync(legacy.runPath, 'utf8');
    const beforeService = readFileSync(path.join(installDir, 'cpa-manager-plus.service'), 'utf8');

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
          ...(expected === 'did not become healthy'
            ? { FAKE_NATIVE_EMPTY_PID_FILE: path.join(installDir, 'cpa-manager-plus.pid') }
            : {}),
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
      expect(readFileSync(legacy.cpaKeyPath, 'utf8')).toBe('cpa_existing_management_key\n');
      expect(existsSync(pendingPath)).toBe(true);
      expect(statSync(pendingPath).mode & 0o777).toBe(0o600);
      expect(readFileSync(pendingPath, 'utf8')).not.toContain('cpa_existing_management_key');
      expect(readFileSync(pendingPath, 'utf8')).toContain('CPA_URL=http://127.0.0.1:8317');
      expect(existsSync(path.join(installDir, 'cpa-manager-plus.pid'))).toBe(false);
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
      expect(combinedOutput(result)).toContain(
        'Automatic native upgrade rollback did not fully succeed'
      );
      expect(readFileSync(legacy.dbPath, 'utf8')).toContain(
        'existing-usage-data\nimport-attempt\n'
      );
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
    const packageName = `cpa-manager-plus_v1.2.3_${platform}_${arch}`;
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
    https://raw.githubusercontent.com/seakee/CPA-Manager-Plus/update-channel/stable-version.txt)
      printf 'v1.2.3'
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
    const packageName = `cpa-manager-plus_v1.2.3_${platform}_${arch}`;
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
  if [ "$arg" = "https://raw.githubusercontent.com/seakee/CPA-Manager-Plus/update-channel/stable-version.txt" ]; then
    printf 'v1.2.3'
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
      const binaryDir = path.join(installDir, 'runtime', 'cpa-manager-plus_v1.8.1_linux_amd64');
      const escapedBinaryDir = binaryDir.replaceAll('%', '%%');
      expect(service).toContain(`WorkingDirectory="${escapedBinaryDir}"`);
      expect(service).toContain(`ExecStart="${escapedBinaryDir}/cpa-manager-plus"`);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
    }
  });

  it('retries a failed first Docker CPA import from the retained pending state', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const statePath = path.join(installDir, 'secrets/cpa-connection-import.pending');
    const keyPath = path.join(installDir, 'secrets/cpa-management-key');

    try {
      writeFakeDocker(fakeBin);
      const commonEnv = {
        ...process.env,
        CPAMP_NON_INTERACTIVE: '1',
        CPAMP_CONFIRM: '1',
        CPAMP_LANG: 'en-US',
        CPAMP_INSTALL_DIR: installDir,
        FAKE_DOCKER_LOG: dockerLog,
        PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
      };
      const first = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...commonEnv,
          CPAMP_INSTALL_MODE: 'cpamp',
          CPAMP_DEPLOY_METHOD: 'docker',
          CPAMP_CPA_CONNECTION_MODE: 'env',
          CPAMP_CPA_URL: 'http://host.docker.internal:8317',
          CPAMP_CPA_MANAGEMENT_KEY: 'cpa_retry_key',
          FAKE_DOCKER_IMPORT_OK: '0',
        },
        encoding: 'utf8',
      });

      expect(first.status).toBe(1);
      expect(existsSync(statePath)).toBe(true);
      expect(statSync(statePath).mode & 0o777).toBe(0o600);
      expect(readFileSync(statePath, 'utf8')).not.toContain('cpa_retry_key');
      expect(readFileSync(statePath, 'utf8')).toContain('CPA_URL=http://host.docker.internal:8317');
      expect(readFileSync(keyPath, 'utf8')).toBe('cpa_retry_key\n');

      const second = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...commonEnv,
          CPAMP_OPERATION: 'upgrade',
          FAKE_DOCKER_IMPORT_OK: '1',
        },
        encoding: 'utf8',
      });

      expect(second.status).toBe(0);
      expect(combinedOutput(second)).toContain('will be retried automatically');
      expect(readFileSync(dockerLog, 'utf8').match(/store-cpa-connection/g) || []).toHaveLength(2);
      expect(existsSync(statePath)).toBe(false);
      expect(existsSync(keyPath)).toBe(false);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('retries a failed first native CPA import from the retained pending state', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const release = writeFakeNativeRelease();
    const commandLog = path.join(installDir, 'native-command.log');
    const dbPath = path.join(installDir, 'data/usage.sqlite');
    const statePath = path.join(installDir, 'secrets/cpa-connection-import.pending');
    const keyPath = path.join(installDir, 'secrets/cpa-management-key');

    try {
      const commonEnv = {
        ...process.env,
        CPAMP_NON_INTERACTIVE: '1',
        CPAMP_CONFIRM: '1',
        CPAMP_LANG: 'en-US',
        CPAMP_VERSION: 'vnext',
        CPAMP_INSTALL_DIR: installDir,
        CPAMP_FAKE_NATIVE_ARCHIVE: release.archivePath,
        FAKE_NATIVE_COMMAND_LOG: commandLog,
        FAKE_NATIVE_DB_PATH: dbPath,
        PATH: `${release.fakeBin}${path.delimiter}${process.env.PATH || ''}`,
      };
      const first = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...commonEnv,
          CPAMP_INSTALL_MODE: 'cpamp',
          CPAMP_DEPLOY_METHOD: 'native',
          CPAMP_CPA_CONNECTION_MODE: 'env',
          CPAMP_CPA_URL: 'http://127.0.0.1:8317',
          CPAMP_CPA_MANAGEMENT_KEY: 'cpa_native_retry_key',
          FAKE_NATIVE_IMPORT_OK: '0',
        },
        encoding: 'utf8',
      });

      expect(first.status).toBe(1);
      expect(existsSync(statePath)).toBe(true);
      expect(readFileSync(statePath, 'utf8')).not.toContain('cpa_native_retry_key');
      expect(readFileSync(keyPath, 'utf8')).toBe('cpa_native_retry_key\n');

      const second = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...commonEnv,
          CPAMP_OPERATION: 'upgrade',
          FAKE_NATIVE_IMPORT_OK: '1',
        },
        encoding: 'utf8',
      });

      expect(second.status).toBe(0);
      expect(combinedOutput(second)).toContain('will be retried automatically');
      expect(readFileSync(commandLog, 'utf8').trim().split('\n')).toHaveLength(2);
      expect(existsSync(statePath)).toBe(false);
      expect(existsSync(keyPath)).toBe(false);
    } finally {
      stopNativeFixtureProcess(installDir);
      rmSync(installDir, { recursive: true, force: true });
      rmSync(release.fakeBin, { recursive: true, force: true });
      rmSync(release.fixtureDir, { recursive: true, force: true });
    }
  });

  it('migrates mixed-case native CPA fields and removes every semantic variant', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const release = writeFakeNativeRelease();
    const legacy = writeLegacyNativeInstall(installDir);
    const commandLog = path.join(installDir, 'native-command.log');
    const config = JSON.parse(readFileSync(legacy.configPath, 'utf8'));
    delete config.cpaUpstreamUrl;
    delete config.managementKeyFile;
    config.CPAUpstreamURL = 'http://127.0.0.1:8317';
    config.ManagementKeyFile = '../../secrets/cpa-management-key';
    writeFileSync(legacy.configPath, `${JSON.stringify(config, null, 2)}\n`);

    try {
      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
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
      expect(readFileSync(commandLog, 'utf8')).toContain('store-cpa-connection');
      const upgraded = JSON.parse(
        readFileSync(path.join(installDir, 'runtime', release.packageName, 'config.json'), 'utf8')
      );
      expect(
        Object.keys(upgraded).some((key) =>
          ['cpaupstreamurl', 'managementkeyfile'].includes(key.toLowerCase())
        )
      ).toBe(false);
    } finally {
      stopNativeFixtureProcess(installDir);
      rmSync(installDir, { recursive: true, force: true });
      rmSync(release.fakeBin, { recursive: true, force: true });
      rmSync(release.fixtureDir, { recursive: true, force: true });
    }
  });

  it('rejects duplicate case-insensitive native CPA fields before changing the install', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const legacy = writeLegacyNativeInstall(installDir);
    const beforeRun = readFileSync(legacy.runPath, 'utf8');
    const config = JSON.parse(readFileSync(legacy.configPath, 'utf8'));
    config.CPAUpstreamURL = 'http://conflicting.example:8317';
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
      expect(combinedOutput(result)).toContain('exactly one case-insensitive cpaUpstreamUrl field');
      expect(readFileSync(legacy.runPath, 'utf8')).toBe(beforeRun);
      expect(existsSync(legacy.cpaKeyPath)).toBe(true);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
    }
  });

  it('recovers a pre-pending Docker failure only with an explicit CPA URL', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const fakeBin = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-bin-'));
    const dockerLog = path.join(
      os.tmpdir(),
      `cpamp-installer-docker-${process.pid}-${Date.now()}.log`
    );
    const keyPath = path.join(installDir, 'secrets/cpa-management-key');

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
      writeFileSync(keyPath, 'cpa_legacy_failed_key\n');
      chmodSync(keyPath, 0o600);
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
          CPAMP_CPA_CONNECTION_MODE: 'env',
          CPAMP_CPA_URL: 'http://host.docker.internal:8317',
          FAKE_DOCKER_LOG: dockerLog,
          PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ''}`,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(0);
      expect(readFileSync(dockerLog, 'utf8')).toContain('store-cpa-connection');
      expect(existsSync(path.join(installDir, 'secrets/cpa-connection-import.pending'))).toBe(
        false
      );
      expect(existsSync(keyPath)).toBe(false);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(fakeBin, { recursive: true, force: true });
      rmSync(dockerLog, { force: true });
    }
  });

  it('rejects a symlinked CPA import pending state without touching its target', () => {
    const installDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-installer-'));
    const externalDir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-external-state-'));
    const externalState = path.join(externalDir, 'pending-state');
    const pendingPath = path.join(installDir, 'secrets/cpa-connection-import.pending');

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
      writeFileSync(externalState, 'external-state-content\n');
      chmodSync(externalState, 0o640);
      symlinkSync(externalState, pendingPath);

      const result = spawnSync('bash', [installerPath], {
        cwd: repoRoot,
        env: {
          ...process.env,
          CPAMP_OPERATION: 'upgrade',
          CPAMP_NON_INTERACTIVE: '1',
          CPAMP_CONFIRM: '1',
          CPAMP_LANG: 'en-US',
          CPAMP_INSTALL_DIR: installDir,
        },
        encoding: 'utf8',
      });

      expect(result.status).toBe(1);
      expect(combinedOutput(result)).toContain('not an installer-owned regular file');
      expect(readFileSync(externalState, 'utf8')).toBe('external-state-content\n');
      expect(statSync(externalState).mode & 0o777).toBe(0o640);
      expect(lstatSync(pendingPath).isSymbolicLink()).toBe(true);
    } finally {
      rmSync(installDir, { recursive: true, force: true });
      rmSync(externalDir, { recursive: true, force: true });
    }
  });
});
