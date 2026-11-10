#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# The Dockerfiles fail closed on identity; bind a local build to this checkout.
export VERSION="${VERSION:-dev}"
export SOURCE_COMMIT="${SOURCE_COMMIT:-$(git -C "${repo_root}" rev-parse HEAD)}"
project="${CPAMP_SMOKE_PROJECT:-cpamp-runtime13-${RANDOM}-$$}"
public_port="${CPAMP_SMOKE_PORT:-28317}"
skip_build="${CPAMP_SMOKE_SKIP_BUILD:-false}"
snapshot_root="$(mktemp -d "${TMPDIR:-/tmp}/cpamp-runtime13-smoke.XXXXXX")"
initializer_name="${project}-cpamp-manager-init"
# Explicit historical baseline; the normal Docker/Compose default is CPA v8.
compose=(docker compose --project-directory "${repo_root}" -f "${repo_root}/docker-compose.yml" -f "${repo_root}/bin/ci/runtime-v7-compatibility.compose.yml" -p "${project}")

cleanup() {
  docker rm -f "${initializer_name}" >/dev/null 2>&1 || true
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
  "${compose[@]}" exec -T cpa-manager-plus sh -ec '
    token_file="${CPAMP_RUNTIME_TOKEN_FILE:-}"
    if [ -z "${token_file}" ] || [ ! -e "${token_file}" ]; then
      for candidate in /data/runtime-secret/token /run/cpamp/runtime-secret/token /data/.cpamp-runtime-secret/token /data/runtime/supervisor/token; do
        if [ -e "${candidate}" ]; then token_file="${candidate}"; break; fi
      done
    fi
    token="$(cat "${token_file}")"
    wget -qO- \
      --header="Authorization: Bearer ${token}" \
      --header="X-CPAMP-Runtime-Features: active-gateway-artifact-v1" \
      "http://127.0.0.1:9081${1}"
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
  "${compose[@]}" exec -T cpa-manager-plus sh -ec '
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
  docker cp "${container_id}:${source}-wal" "${destination}-wal" >/dev/null 2>&1 || true
  docker cp "${container_id}:${source}-shm" "${destination}-shm" >/dev/null 2>&1 || true
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
    if not operation_id.startswith("runtime-reconcile/v2:"):
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
# Phase4-01 ordinary Fresh startup is deliberately read-only. Prove the
# Manager listener exposes fixed discovery without manufacturing readiness or
# product state before an explicit initializer is selected.
"${compose[@]}" up -d cpa-manager-plus
retry 60 "${compose[@]}" exec -T cpa-manager-plus sh -ec '
  body="$(wget -qO- http://127.0.0.1:18317/.well-known/cpamp-bootstrap)"
  printf "%s" "$body" | grep -q "\"status\":\"fresh_claim_required\""
  printf "%s" "$body" | grep -q "\"mode\":\"fresh\""
'
if "${compose[@]}" exec -T cpa-manager-plus wget -qO- http://127.0.0.1:18317/health >/dev/null 2>&1; then
  echo "Ordinary Fresh startup became ready before explicit initialization" >&2
  exit 1
fi
"${compose[@]}" exec -T cpa-manager-plus sh -ec '
  test ! -e /data/usage.sqlite
  test ! -e /data/data.key
  test ! -e /data/usage.sqlite.manager.lock
'
# Verify the documented in-place initializer command form as uid/gid 10002:10002
# without any post-hoc ownership repair, then verify Manager bootstrap admits the
# store and denies CPA/Ingress read access to /data/usage.sqlite and /data/data.key.
doc_handoff="$("${compose[@]}" exec -T -u 10002:10002 cpa-manager-plus cpa-manager-plus initialize)"
case "${doc_handoff}" in
  "/.well-known/cpamp-bootstrap/ui#handoff="*) ;;
  *)
    echo "Documented initializer command did not emit the expected fixed Bootstrap fragment handoff" >&2
    exit 1
    ;;
esac
retry 30 "${compose[@]}" exec -T cpa-manager-plus sh -ec '
  wget -qO- http://127.0.0.1:18317/health >/dev/null
  body="$(wget -qO- http://127.0.0.1:18317/.well-known/cpamp-bootstrap)"
  printf "%s" "$body" | grep -q "\"status\":\"fresh_claim_required\""
  printf "%s" "$body" | grep -q "\"mode\":\"fresh\""
'
"${compose[@]}" exec -T -u 10001:10001 cpa-manager-plus sh -ec '! test -r /data/usage.sqlite && ! test -r /data/data.key'
"${compose[@]}" exec -T -u 10003:10003 cpa-manager-plus sh -ec '! test -r /data/usage.sqlite && ! test -r /data/data.key'
"${compose[@]}" stop cpa-manager-plus >/dev/null
"${compose[@]}" down --volumes --remove-orphans >/dev/null
# Verify the one-shot Fresh admission/handoff contract, then convert only this
# disposable CI volume to the explicit historical Manager baseline required by
# the existing Runtime lifecycle smoke. Fresh itself must not select Embedded
# Runtime or create runtime_desired_v1.
"${repo_root}/bin/ci/runtime-lifecycle-manager-baseline.sh" "${project}" "${initializer_name}"

# The historical baseline retains the existing Runtime lifecycle checks:
# normal Manager compatibility startup must create the desired state and start
# CPA without a direct Supervisor mutation from the smoke test.
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
active_artifact_id="$(printf '%s' "$ready_status" | json_field activeGatewayArtifact.artifactId)"
if [[ ! "$active_artifact_id" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "Runtime status returned a noncanonical active artifact ID: $active_artifact_id" >&2
  exit 1
fi
if [ "$(printf '%s' "$ready_status" | json_field activeGatewayArtifact.engine)" != "cpa" ] ||
  [ "$(printf '%s' "$ready_status" | json_field activeGatewayArtifact.version)" != "7.3.3" ] ||
  [ "$(printf '%s' "$ready_status" | json_field cpaObservedVersion)" != "7.3.3" ]; then
  echo "Runtime status did not expose the trusted bundled CPA v7.3.3 artifact" >&2
  exit 1
fi
executable_artifact_id="sha256:$("${compose[@]}" exec -T cpa-manager-plus sha256sum /usr/local/bin/cli-proxy-api | awk '{print $1}')"
if [ "$active_artifact_id" != "$executable_artifact_id" ]; then
  echo "Runtime active artifact ID does not match exact CPA executable bytes" >&2
  exit 1
fi

"${compose[@]}" exec -T cpa-manager-plus sh -ec '
  test "$(tr "\000" "\n" </proc/1/cmdline | head -n1)" = "/usr/local/bin/cpamp-launcher"
  rt_root="/data/runtime"
  [ -d "${rt_root}" ] || rt_root="/runtime"
  token_file="${CPAMP_RUNTIME_TOKEN_FILE:-}"
  if [ -z "${token_file}" ] || [ ! -e "${token_file}" ]; then
    for candidate in /data/runtime-secret/token /run/cpamp/runtime-secret/token /data/.cpamp-runtime-secret/token "${rt_root}/supervisor/token"; do
      if [ -e "${candidate}" ]; then token_file="${candidate}"; break; fi
    done
  fi
  test "$(wc -c <"${token_file}" | tr -d " ")" -ge 32
  test -f "${rt_root}/gateway/config.yaml"
  test "$(stat -c %a "${rt_root}/gateway/config.yaml")" = "600"
  test -d "${rt_root}/supervisor"
  test -d "${rt_root}/gateway/auth"
  ! grep -Eq "secret-key: +[^\" ]|api-keys: +\[[^]]" "${rt_root}/gateway/config.yaml"
'

healthy_cpa_pid="$(runtime_cpa_pid)"
"${compose[@]}" exec -T cpa-manager-plus sh -ec 'kill -TERM $(pidof cpa-manager-plus)'
retry 90 curl --fail --silent "${public_origin}/health" >/dev/null
sleep 7
if [ "$(runtime_cpa_pid)" != "$healthy_cpa_pid" ]; then
  echo "Manager restart replaced a healthy CPA child" >&2
  exit 1
fi
manager_restart_status="$(wait_runtime_state ready 30)"
if [ "$(printf '%s' "$manager_restart_status" | json_field activeGatewayArtifact.artifactId)" != "$active_artifact_id" ]; then
  echo "Manager restart changed the active Gateway artifact ID" >&2
  exit 1
fi

"${compose[@]}" exec -T cpa-manager-plus sh -ec 'kill -KILL $(pidof cpa-manager-plus)'
retry 90 curl --fail --silent "${public_origin}/health" >/dev/null
sleep 7
if [ "$(runtime_cpa_pid)" != "$healthy_cpa_pid" ]; then
  echo "Manager recreate replaced a healthy CPA child" >&2
  exit 1
fi
manager_recreate_status="$(wait_runtime_state ready 30)"
if [ "$(printf '%s' "$manager_recreate_status" | json_field activeGatewayArtifact.artifactId)" != "$active_artifact_id" ]; then
  echo "Manager recreate changed the active Gateway artifact ID" >&2
  exit 1
fi

"${compose[@]}" exec -T cpa-manager-plus sh -ec 'kill -STOP $(pidof cpa-manager-plus)'
curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null
copy_container_file cpa-manager-plus /data/usage.sqlite "${snapshot_root}/manager.sqlite"
assert_manager_desired_running "${snapshot_root}/manager.sqlite"
"${compose[@]}" exec -T cpa-manager-plus sh -ec 'kill -CONT $(pidof cpa-manager-plus)'
retry 90 curl --fail --silent "${public_origin}/health" >/dev/null
retry 30 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null
if [ "$(runtime_cpa_pid)" != "$healthy_cpa_pid" ]; then
  echo "Manager stop/start replaced a healthy CPA child" >&2
  exit 1
fi
manager_start_status="$(wait_runtime_state ready 30)"
if [ "$(printf '%s' "$manager_start_status" | json_field activeGatewayArtifact.artifactId)" != "$active_artifact_id" ]; then
  echo "Manager stop/start changed the active Gateway artifact ID" >&2
  exit 1
fi

runtime_root="$("${compose[@]}" exec -T cpa-manager-plus sh -ec 'if [ -d /data/runtime ]; then printf /data/runtime; else printf /runtime; fi')"
token_file="$("${compose[@]}" exec -T cpa-manager-plus sh -ec 'for c in /data/runtime-secret/token /run/cpamp/runtime-secret/token /data/.cpamp-runtime-secret/token /data/runtime/supervisor/token; do if [ -e "$c" ]; then printf "%s" "$c"; break; fi; done')"
secret_before="$("${compose[@]}" exec -T cpa-manager-plus sha256sum "${token_file}" | awk '{print $1}')"
"${compose[@]}" exec -T cpa-manager-plus sh -ec '
  printf "\n# runtime13-smoke-existing-config-marker\n" >> "${1}/gateway/config.yaml"
' sh "${runtime_root}"
config_before="$("${compose[@]}" exec -T cpa-manager-plus sha256sum "${runtime_root}/gateway/config.yaml" | awk '{print $1}')"

copy_container_file cpa-manager-plus "${runtime_root}/supervisor/operations.sqlite" "${snapshot_root}/operations-before.sqlite"
assert_reconcile_start_journal "${snapshot_root}/operations-before.sqlite" 1

"${compose[@]}" stop cpa-manager-plus >/dev/null
"${compose[@]}" up -d --force-recreate cpa-manager-plus >/dev/null
ready_after_recreate="$(wait_runtime_state ready 120)"
generation_after_runtime_recreate="$(printf '%s' "$ready_after_recreate" | json_uint_field runtimeGeneration)"
if [ "$generation_after_runtime_recreate" = "$generation_before_runtime_recreate" ]; then
  echo "Runtime recreate reused the Supervisor generation" >&2
  exit 1
fi
if [ "$(printf '%s' "$ready_after_recreate" | json_field activeGatewayArtifact.artifactId)" != "$active_artifact_id" ]; then
  echo "Supervisor recreation changed the unchanged active Gateway artifact ID" >&2
  exit 1
fi
retry 30 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null

secret_after="$("${compose[@]}" exec -T cpa-manager-plus sha256sum "${token_file}" | awk '{print $1}')"
config_after="$("${compose[@]}" exec -T cpa-manager-plus sha256sum "${runtime_root}/gateway/config.yaml" | awk '{print $1}')"
if [ "$secret_before" != "$secret_after" ]; then
  echo "Runtime transport secret changed across Runtime recreate" >&2
  exit 1
fi
if [ "$config_before" != "$config_after" ]; then
  echo "Existing Gateway configuration was overwritten across Runtime recreate" >&2
  exit 1
fi

copy_container_file cpa-manager-plus "${runtime_root}/supervisor/operations.sqlite" "${snapshot_root}/operations-after.sqlite"
assert_reconcile_start_journal "${snapshot_root}/operations-after.sqlite" 2
echo "Runtime 13 Docker smoke passed: read-only Fresh discovery, explicit initialization, persistent desired state, stable healthy child across Manager lifecycle, generation-scoped Runtime recovery, single public ingress, and isolated health"
