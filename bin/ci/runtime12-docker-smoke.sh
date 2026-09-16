#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
project="${CPAMP_SMOKE_PROJECT:-cpamp-runtime13-${RANDOM}-$$}"
public_port="${CPAMP_SMOKE_PORT:-28317}"
skip_build="${CPAMP_SMOKE_SKIP_BUILD:-false}"
snapshot_root="$(mktemp -d "${TMPDIR:-/tmp}/cpamp-runtime13-smoke.XXXXXX")"
compose=(docker compose --project-directory "${repo_root}" -p "${project}")

cleanup() {
  "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
  rm -rf "${snapshot_root}"
}
trap cleanup EXIT HUP INT TERM

retry() {
  local attempts="$1"
  shift
  local count=0
  until "$@"; do
    count=$((count + 1))
    if [ "$count" -ge "$attempts" ]; then
      return 1
    fi
    sleep 1
  done
}

runtime_get() {
  local request_path="$1"
  "${compose[@]}" exec -T cpamp-manager sh -ec '
    token="$(cat /run/cpamp/runtime-secret/token)"
    wget -qO- --header="Authorization: Bearer ${token}" "http://cpamp-runtime:9081${1}"
  ' sh "$request_path"
}

json_field() {
  local field="$1"
  node -e '
    const value = JSON.parse(require("node:fs").readFileSync(0, "utf8"));
    const fields = process.argv[1].split(".");
    let current = value;
    for (const field of fields) current = current?.[field];
    if (current === undefined || current === null) process.exit(2);
    process.stdout.write(String(current));
  ' "$field"
}

json_uint_field() {
  local field="$1"
  node -e '
    const input = require("node:fs").readFileSync(0, "utf8");
    const match = input.match(new RegExp(`"${process.argv[1]}"\\s*:\\s*(\\d+)`));
    if (!match) process.exit(2);
    process.stdout.write(match[1]);
  ' "$field"
}

wait_runtime_state() {
  local expected="$1"
  local attempts="${2:-90}"
  local count=0
  while [ "$count" -lt "$attempts" ]; do
    local status
    if status="$(runtime_get /v1/runtime/status 2>/dev/null)" && [ "$(printf '%s' "$status" | json_field state)" = "$expected" ]; then
      printf '%s' "$status"
      return 0
    fi
    count=$((count + 1))
    sleep 1
  done
  return 1
}

runtime_cpa_pid() {
  "${compose[@]}" exec -T cpamp-runtime sh -ec '
    for process_dir in /proc/[0-9]*; do
      if [ -r "${process_dir}/comm" ] && [ "$(cat "${process_dir}/comm")" = "cli-proxy-api" ]; then
        printf "%s" "${process_dir##*/}"
        exit 0
      fi
    done
    exit 1
  '
}

copy_container_file() {
  local service="$1"
  local source="$2"
  local destination="$3"
  local container_id
  container_id="$("${compose[@]}" ps --all -q "$service")"
  test -n "$container_id"
  docker cp "${container_id}:${source}" "$destination" >/dev/null
}

assert_manager_desired_running() {
  local database_path="$1"
  python3 - "$database_path" <<'PY'
import json
import sqlite3
import sys

connection = sqlite3.connect(sys.argv[1])
row = connection.execute(
    "select value, updated_at_ms from settings where key = 'runtime_desired_v1'"
).fetchone()
if row is None:
    raise SystemExit("runtime_desired_v1 is missing")
value = json.loads(row[0])
if value.get("desiredLifecycle") != "running":
    raise SystemExit(f"unexpected desired lifecycle: {value!r}")
if not isinstance(value.get("revision"), int) or value["revision"] <= 0:
    raise SystemExit(f"invalid desired revision: {value!r}")
if not isinstance(value.get("updatedAtMs"), int) or value["updatedAtMs"] <= 0:
    raise SystemExit(f"invalid desired timestamp: {value!r}")
if row[1] != value["updatedAtMs"]:
    raise SystemExit(f"setting timestamp mismatch: row={row[1]} value={value!r}")
PY
}

assert_reconcile_start_journal() {
  local database_path="$1"
  local minimum_count="$2"
  python3 - "$database_path" "$minimum_count" <<'PY'
import sqlite3
import sys

connection = sqlite3.connect(sys.argv[1])
rows = connection.execute(
    "select operation_id, runtime_generation, state from operations "
    "where operation_type = 'start' order by created_at_ms"
).fetchall()
if len(rows) < int(sys.argv[2]):
    raise SystemExit(f"expected at least {sys.argv[2]} Start operations, got {rows!r}")
ids = []
generations = []
for operation_id, generation, state in rows:
    if isinstance(operation_id, bytes):
        operation_id = operation_id.decode("utf-8")
    if not operation_id.startswith("runtime-reconcile/v1:"):
        raise SystemExit(f"non-reconcile Start operation ID: {operation_id!r}")
    if state != "succeeded":
        raise SystemExit(f"Start operation is not succeeded: {(operation_id, state)!r}")
    ids.append(operation_id)
    generations.append(generation)
if len(set(ids)) != len(ids):
    raise SystemExit(f"duplicate reconcile operation IDs: {ids!r}")
if len(rows) >= 2 and len(set(generations)) < 2:
    raise SystemExit("Runtime restart did not create generation-scoped Start evidence")
PY
}

cd "$repo_root"
CPAMP_PUBLIC_PORT=18317 "${compose[@]}" config --format json | node bin/ci/validate-runtime12-compose.mjs
docker compose -f docker-compose.manager.yml config >/dev/null
export CPAMP_PUBLIC_PORT="${public_port}"
if [ "$skip_build" != "true" ]; then
  "${compose[@]}" build
fi
docker run --rm \
  --env CPAMP_RUNTIME_TOKEN=runtime13-ci-test-only-transport-secret \
  seakee/cpamp-runtime:latest \
  sh -ec 'test "$(cat /run/cpamp/runtime-secret/token)" = "$CPAMP_RUNTIME_TOKEN"'

# Start Manager before Runtime creates the token. Manager health and its HTTP
# listener must remain independent from this expected Compose startup race.
"${compose[@]}" up -d cpamp-manager
retry 60 "${compose[@]}" exec -T cpamp-manager wget -qO- http://127.0.0.1:18317/health >/dev/null

# This is the ordinary fresh Embedded stack path. The test never submits a
# Supervisor mutation; Manager desired-state reconciliation must start CPA.
"${compose[@]}" up -d
public_origin="http://127.0.0.1:${public_port}"
retry 90 curl --fail --silent "${public_origin}/health" >/dev/null
retry 120 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null

ready_status="$(wait_runtime_state ready 120)"
if [ "$(printf '%s' "$ready_status" | json_field recovery.state)" != "armed" ]; then
  echo "Manager typed Start did not arm bounded recovery" >&2
  exit 1
fi
generation_before_runtime_recreate="$(printf '%s' "$ready_status" | json_uint_field runtimeGeneration)"

"${compose[@]}" exec -T cpamp-runtime sh -ec '
  test "$(tr "\000" "\n" </proc/1/cmdline | head -n1)" = "/usr/local/bin/cpamp-runtime-supervisor"
  test "$(stat -c %a /run/cpamp/runtime-secret/token)" = "600"
  test "$(wc -c </run/cpamp/runtime-secret/token | tr -d " ")" -ge 32
  test -f /runtime/gateway/config.yaml
  test "$(stat -c %a /runtime/gateway/config.yaml)" = "600"
  test -d /runtime/supervisor
  test -d /runtime/gateway/auth
  ! grep -Eq "secret-key: +[^\" ]|api-keys: +\[[^]]" /runtime/gateway/config.yaml
'

healthy_cpa_pid="$(runtime_cpa_pid)"
"${compose[@]}" restart cpamp-manager >/dev/null
retry 90 curl --fail --silent "${public_origin}/health" >/dev/null
sleep 7
if [ "$(runtime_cpa_pid)" != "$healthy_cpa_pid" ]; then
  echo "Manager restart replaced a healthy CPA child" >&2
  exit 1
fi

"${compose[@]}" up -d --force-recreate cpamp-manager >/dev/null
retry 90 curl --fail --silent "${public_origin}/health" >/dev/null
sleep 7
if [ "$(runtime_cpa_pid)" != "$healthy_cpa_pid" ]; then
  echo "Manager recreate replaced a healthy CPA child" >&2
  exit 1
fi

"${compose[@]}" stop cpamp-manager >/dev/null
curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null
copy_container_file cpamp-manager /data/usage.sqlite "${snapshot_root}/manager.sqlite"
assert_manager_desired_running "${snapshot_root}/manager.sqlite"
"${compose[@]}" start cpamp-manager >/dev/null
retry 90 curl --fail --silent "${public_origin}/health" >/dev/null
retry 30 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null
if [ "$(runtime_cpa_pid)" != "$healthy_cpa_pid" ]; then
  echo "Manager stop/start replaced a healthy CPA child" >&2
  exit 1
fi

secret_before="$("${compose[@]}" exec -T cpamp-manager sha256sum /run/cpamp/runtime-secret/token | awk '{print $1}')"
"${compose[@]}" exec -T cpamp-runtime sh -ec '
  printf "\n# runtime13-smoke-existing-config-marker\n" >> /runtime/gateway/config.yaml
'
config_before="$("${compose[@]}" exec -T cpamp-runtime sha256sum /runtime/gateway/config.yaml | awk '{print $1}')"

"${compose[@]}" stop cpamp-runtime >/dev/null
curl --fail --silent --show-error "${public_origin}/health" >/dev/null
copy_container_file cpamp-runtime /runtime/supervisor/operations.sqlite "${snapshot_root}/operations-before.sqlite"
assert_reconcile_start_journal "${snapshot_root}/operations-before.sqlite" 1

"${compose[@]}" up -d --force-recreate cpamp-runtime >/dev/null
ready_after_recreate="$(wait_runtime_state ready 120)"
generation_after_runtime_recreate="$(printf '%s' "$ready_after_recreate" | json_uint_field runtimeGeneration)"
if [ "$generation_after_runtime_recreate" = "$generation_before_runtime_recreate" ]; then
  echo "Runtime recreate reused the Supervisor generation" >&2
  exit 1
fi
retry 30 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null

secret_after="$("${compose[@]}" exec -T cpamp-manager sha256sum /run/cpamp/runtime-secret/token | awk '{print $1}')"
config_after="$("${compose[@]}" exec -T cpamp-runtime sha256sum /runtime/gateway/config.yaml | awk '{print $1}')"
if [ "$secret_before" != "$secret_after" ]; then
  echo "Runtime transport secret changed across Runtime recreate" >&2
  exit 1
fi
if [ "$config_before" != "$config_after" ]; then
  echo "Existing Gateway configuration was overwritten across Runtime recreate" >&2
  exit 1
fi

"${compose[@]}" stop cpamp-runtime >/dev/null
curl --fail --silent --show-error "${public_origin}/health" >/dev/null
copy_container_file cpamp-runtime /runtime/supervisor/operations.sqlite "${snapshot_root}/operations-after.sqlite"
assert_reconcile_start_journal "${snapshot_root}/operations-after.sqlite" 2

echo "Runtime 13 Docker smoke passed: fresh Manager-owned Start, persistent desired state, stable healthy child across Manager lifecycle, generation-scoped Runtime recovery, single public ingress, and isolated health"
