#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
project="${CPAMP_RUNTIME14_PROJECT:-cpamp-runtime14-${RANDOM}-$$}"
public_port="${CPAMP_RUNTIME14_PORT:-29317}"
skip_build="${CPAMP_RUNTIME14_SKIP_BUILD:-false}"
initializer_name="${project}-cpamp-manager-init"
snapshot_root="$(mktemp -d "${TMPDIR:-/tmp}/cpamp-runtime14-failure.XXXXXX")"
compose=(docker compose --project-directory "${repo_root}" -p "${project}")

paused_manager=""
stopped_cpa_pid=""
disconnected_network=""
disconnected_runtime=""
executable_moved="false"
cpa_executable="/usr/local/bin/cli-proxy-api"
cpa_restore_path="/usr/local/bin/cli-proxy-api.runtime14-restore"

cleanup() {
  set +e
  docker rm -f "${initializer_name}" >/dev/null 2>&1 || true
  if [ -n "${stopped_cpa_pid}" ]; then
    "${compose[@]}" exec -T cpamp-runtime sh -ec 'kill -CONT "$1"' sh "${stopped_cpa_pid}" >/dev/null 2>&1
  fi
  if [ "${executable_moved}" = "true" ]; then
    "${compose[@]}" exec -T cpamp-runtime sh -ec '
      if [ -e "$2" ] && [ ! -e "$1" ]; then mv "$2" "$1"; fi
    ' sh "${cpa_executable}" "${cpa_restore_path}" >/dev/null 2>&1
  fi
  if [ -n "${paused_manager}" ]; then docker unpause "${paused_manager}" >/dev/null 2>&1; fi
  if [ -n "${disconnected_network}" ] && [ -n "${disconnected_runtime}" ]; then
    docker network connect --alias cpamp-runtime "${disconnected_network}" "${disconnected_runtime}" >/dev/null 2>&1
  fi
  "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1
  rm -rf "${snapshot_root}"
}
trap cleanup EXIT HUP INT TERM

fail() {
  echo "Runtime14 failure E2E: $*" >&2
  exit 1
}

retry() {
  local attempts="$1"
  shift
  local count=0
  until "$@"; do
    count=$((count + 1))
    if [ "${count}" -ge "${attempts}" ]; then
      return 1
    fi
    sleep 1
  done
}

assert_equal() {
  local actual="$1"
  local expected="$2"
  local message="$3"
  if [ "${actual}" != "${expected}" ]; then
    fail "${message}: got '${actual}', want '${expected}'"
  fi
}

assert_not_equal() {
  local actual="$1"
  local unexpected="$2"
  local message="$3"
  if [ "${actual}" = "${unexpected}" ]; then
    fail "${message}: both values are '${actual}'"
  fi
}

assert_greater_than() {
  local actual="$1"
  local minimum="$2"
  local message="$3"
  if [ "${actual}" -le "${minimum}" ]; then
    fail "${message}: got ${actual}, want > ${minimum}"
  fi
}

json_field() {
  local field="$1"
  node -e '
    const value = JSON.parse(require("node:fs").readFileSync(0, "utf8"));
    let current = value;
    for (const segment of process.argv[1].split(".")) current = current?.[segment];
    if (current === undefined || current === null) process.exit(2);
    process.stdout.write(String(current));
  ' "${field}"
}

# RuntimeGeneration is an opaque uint64 and may exceed JavaScript's safe
# integer range, so extract its decimal representation without parsing it.
json_uint_field() {
  local field="$1"
  node -e '
    const input = require("node:fs").readFileSync(0, "utf8");
    const escaped = process.argv[1].replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    const match = input.match(new RegExp(`"${escaped}"\\s*:\\s*(\\d+)`));
    if (!match) process.exit(2);
    process.stdout.write(match[1]);
  ' "${field}"
}

runtime_get_from_manager() {
  local request_path="$1"
  "${compose[@]}" exec -T cpamp-manager sh -ec '
    token="$(cat /run/cpamp/runtime-secret/token)"
    wget -qO- --timeout=4 \
      --header="Authorization: Bearer ${token}" \
      --header="X-CPAMP-Runtime-Features: active-gateway-artifact-v1" \
      "http://cpamp-runtime:9081${1}"
  ' sh "${request_path}"
}

runtime_get_local() {
  local request_path="$1"
  docker run --rm \
    --network "container:${runtime_container}" \
    --volumes-from "${runtime_container}:ro" \
    --entrypoint sh \
    seakee/cpa-manager-plus:latest -ec '
    token="$(cat /run/cpamp/runtime-secret/token)"
    wget -qO- -T 4 \
      --header="Authorization: Bearer ${token}" \
      --header="X-CPAMP-Runtime-Features: active-gateway-artifact-v1" \
      "http://127.0.0.1:9081${1}"
  ' sh "${request_path}"
}

runtime_get() {
  local source="$1"
  local request_path="$2"
  case "${source}" in
    manager) runtime_get_from_manager "${request_path}" ;;
    local) runtime_get_local "${request_path}" ;;
    *) fail "unknown Runtime observation source ${source}" ;;
  esac
}

wait_runtime_observation() {
  local expected_state="$1"
  local expected_recovery="$2"
  local source="${3:-manager}"
  local attempts="${4:-90}"
  local count=0
  while [ "${count}" -lt "${attempts}" ]; do
    local status state recovery
    if status="$(runtime_get "${source}" /v1/runtime/status 2>/dev/null)"; then
      state="$(printf '%s' "${status}" | json_field state 2>/dev/null || true)"
      recovery="$(printf '%s' "${status}" | json_field recovery.state 2>/dev/null || true)"
      if [ "${state}" = "${expected_state}" ] && [ "${recovery}" = "${expected_recovery}" ]; then
        printf '%s' "${status}"
        return 0
      fi
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

assert_cpa_pid_alive() {
  local expected_pid="$1"
  "${compose[@]}" exec -T cpamp-runtime sh -ec '
    test -r "/proc/${1}/comm"
    test "$(cat "/proc/${1}/comm")" = "cli-proxy-api"
  ' sh "${expected_pid}"
}

container_id() {
  local service="$1"
  local id
  id="$("${compose[@]}" ps --all -q "${service}")"
  test -n "${id}"
  printf '%s' "${id}"
}

container_running_by_id() {
  local id="$1"
  [ "$(docker inspect --format '{{.State.Running}}' "${id}" 2>/dev/null)" = "true" ]
}

assert_container_running() {
  local id="$1"
  local description="$2"
  container_running_by_id "${id}" || fail "${description} is not running"
}

public_request() {
  local path="$1"
  curl --fail --silent --show-error --connect-timeout 1 --max-time 4 \
    "http://127.0.0.1:${public_port}${path}"
}

wait_public_success() {
  local path="$1"
  local attempts="${2:-90}"
  retry "${attempts}" public_request "${path}" >/dev/null
}

assert_public_unavailable() {
  local path="$1"
  local attempts="${2:-10}"
  local count=0
  while [ "${count}" -lt "${attempts}" ]; do
    if ! public_request "${path}" >/dev/null 2>&1; then
      return 0
    fi
    count=$((count + 1))
    sleep 1
  done
  fail "public ${path} remained available during the injected outage"
}

copy_sqlite_snapshot() (
  set -euo pipefail
  local service="$1"
  local source="$2"
  local label="$3"
  local destination="${snapshot_root}/${label}"
  local id base
  local paused_id=""

  unpause_snapshot_owner() {
    if [ -n "${paused_id}" ]; then
      docker unpause "${paused_id}" >/dev/null 2>&1 || true
    fi
  }

  trap unpause_snapshot_owner EXIT
  trap 'exit 1' HUP INT TERM

  mkdir -p "${destination}"
  id="$(container_id "${service}")"
  base="${source##*/}"

  docker pause "${id}" >/dev/null
  paused_id="${id}"
  docker cp "${id}:${source}" "${destination}/${base}" >/dev/null
  docker cp "${id}:${source}-wal" "${destination}/${base}-wal" >/dev/null
  docker unpause "${id}" >/dev/null
  paused_id=""
  trap - EXIT HUP INT TERM

  printf '%s' "${destination}/${base}"
)

snapshot_runtime_journal() {
  copy_sqlite_snapshot cpamp-runtime /runtime/supervisor/operations.sqlite "$1"
}

snapshot_manager_database() {
  copy_sqlite_snapshot cpamp-manager /data/usage.sqlite "$1"
}

journal_count() {
  local database_path="$1"
  local operation_type="${2:--}"
  local generation="${3:--}"
  local operation_prefix="${4:--}"
  local state="${5:--}"
  local failure_code="${6:--}"
  python3 - "${database_path}" "${operation_type}" "${generation}" \
    "${operation_prefix}" "${state}" "${failure_code}" <<'PY'
import sqlite3
import sys

database_path, operation_type, generation, operation_prefix, state, failure_code = sys.argv[1:]
conditions = []
parameters = []
if operation_type != "-":
    conditions.append("operation_type = ?")
    parameters.append(operation_type)
if generation != "-":
    conditions.append("runtime_generation = ?")
    parameters.append(int(generation).to_bytes(8, "big"))
if operation_prefix != "-":
    conditions.append("cast(operation_id as text) like ?")
    parameters.append(operation_prefix + "%")
if state != "-":
    conditions.append("state = ?")
    parameters.append(state)
if failure_code != "-":
    conditions.append("failure_code = ?")
    parameters.append(failure_code)
where = " where " + " and ".join(conditions) if conditions else ""
connection = sqlite3.connect(f"file:{database_path}?mode=ro", uri=True)
try:
    value = connection.execute("select count(*) from operations" + where, parameters).fetchone()[0]
finally:
    connection.close()
print(value)
PY
}

assert_manager_starts_are_reconcile_operations() {
  local database_path="$1"
  python3 - "${database_path}" <<'PY'
import sqlite3
import sys

connection = sqlite3.connect(f"file:{sys.argv[1]}?mode=ro", uri=True)
try:
    rows = connection.execute(
        "select cast(operation_id as text), state from operations where operation_type = 'start'"
    ).fetchall()
finally:
    connection.close()
if not rows:
    raise SystemExit("operation journal contains no Manager Start evidence")
for operation_id, state in rows:
    if not operation_id.startswith("runtime-reconcile/v1:"):
        raise SystemExit(f"unexpected Start operation ID: {operation_id!r}")
    if state != "succeeded":
        raise SystemExit(f"Manager Start is not succeeded: {(operation_id, state)!r}")
PY
}

desired_observation() {
  local database_path="$1"
  python3 - "${database_path}" <<'PY'
import json
import sqlite3
import sys

connection = sqlite3.connect(f"file:{sys.argv[1]}?mode=ro", uri=True)
try:
    row = connection.execute(
        "select value, updated_at_ms from settings where key = 'runtime_desired_v1'"
    ).fetchone()
finally:
    connection.close()
if row is None:
    raise SystemExit("runtime_desired_v1 is missing")
value = json.loads(row[0])
if value.get("desiredLifecycle") != "running":
    raise SystemExit(f"unexpected desired lifecycle: {value!r}")
revision = value.get("revision")
updated_at_ms = value.get("updatedAtMs")
if not isinstance(revision, int) or revision <= 0:
    raise SystemExit(f"invalid desired revision: {value!r}")
if not isinstance(updated_at_ms, int) or updated_at_ms <= 0 or updated_at_ms != row[1]:
    raise SystemExit(f"invalid desired timestamp: row={row[1]!r}, value={value!r}")
print(f"running {revision} {updated_at_ms}")
PY
}

assert_lifecycle_counts() {
  local database_path="$1"
  local generation="$2"
  local expected_reconcile_starts="$3"
  local expected_auto_total="$4"
  local expected_restart_total="$5"
  local context="$6"
  local actual
  actual="$(journal_count "${database_path}" start "${generation}" 'runtime-reconcile/v1:' - -)"
  assert_equal "${actual}" "${expected_reconcile_starts}" "${context} reconcile Start count"
  actual="$(journal_count "${database_path}" auto_recovery - - - -)"
  assert_equal "${actual}" "${expected_auto_total}" "${context} auto_recovery count"
  actual="$(journal_count "${database_path}" restart - - - -)"
  assert_equal "${actual}" "${expected_restart_total}" "${context} Restart count"
}

scenario() {
  echo "Runtime14 scenario $1"
}

cd "${repo_root}"
CPAMP_PUBLIC_PORT=18317 "${compose[@]}" config --format json | node bin/ci/validate-runtime12-compose.mjs
docker compose -f docker-compose.manager.yml config >/dev/null
export CPAMP_PUBLIC_PORT="${public_port}"
if [ "${skip_build}" != "true" ]; then
  "${compose[@]}" build
fi

scenario 'A — healthy Manager-owned baseline'
# Runtime14 tests historical Runtime failure behavior, not Fresh product setup.
# Seed a CI-only historical Manager lifecycle baseline without restoring the
# removed implicit Embedded behavior to Phase4-03 initialize.
"${repo_root}/bin/ci/runtime-lifecycle-manager-baseline.sh" "${project}" "${initializer_name}"
"${compose[@]}" up -d
wait_public_success /health 90
wait_public_success /v1/models 120
baseline_status="$(wait_runtime_observation ready armed manager 120)"
baseline_identity="$(printf '%s' "${baseline_status}" | json_field runtimeIdentity)"
baseline_generation="$(printf '%s' "${baseline_status}" | json_uint_field runtimeGeneration)"
baseline_attempts="$(printf '%s' "${baseline_status}" | json_field recovery.attemptsRemaining)"
baseline_artifact_id="$(printf '%s' "$baseline_status" | json_field activeGatewayArtifact.artifactId)"
assert_not_equal "${baseline_generation}" '0' 'baseline RuntimeGeneration must be non-zero'
assert_equal "${baseline_attempts}" '3' 'baseline automatic recovery budget'
if [[ ! "$baseline_artifact_id" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  fail "baseline active artifact ID is noncanonical: $baseline_artifact_id"
fi
assert_equal "$(printf '%s' "$baseline_status" | json_field activeGatewayArtifact.engine)" 'cpa' 'baseline artifact engine'
assert_equal "$(printf '%s' "$baseline_status" | json_field activeGatewayArtifact.version)" '7.3.3' 'baseline trusted artifact version'
assert_equal "$(printf '%s' "$baseline_status" | json_field cpaObservedVersion)" '7.3.3' 'baseline legacy version projection'
runtime_binary_artifact_id="sha256:$("${compose[@]}" exec -T cpamp-runtime sha256sum /usr/local/bin/cli-proxy-api | awk '{print $1}')"
assert_equal "$baseline_artifact_id" "$runtime_binary_artifact_id" 'baseline exact executable artifact ID'
baseline_cpa_pid="$(runtime_cpa_pid)"
assert_cpa_pid_alive "${baseline_cpa_pid}"

ingress_container="$(container_id cpamp-ingress)"
manager_container="$(container_id cpamp-manager)"
runtime_container="$(container_id cpamp-runtime)"
"${compose[@]}" exec -T cpamp-runtime sh -ec '
  test "$(tr "\000" "\n" </proc/1/cmdline | head -n1)" = "/usr/local/bin/cpamp-runtime-supervisor"
'

baseline_manager_database="$(snapshot_manager_database scenario-a-manager)"
read -r baseline_desired baseline_revision baseline_updated_at < <(desired_observation "${baseline_manager_database}")
assert_equal "${baseline_desired}" 'running' 'baseline desired lifecycle'
baseline_journal="$(snapshot_runtime_journal scenario-a-journal)"
assert_lifecycle_counts "${baseline_journal}" "${baseline_generation}" 1 0 0 'scenario A'
assert_equal "$(journal_count "${baseline_journal}" start "${baseline_generation}" 'runtime-reconcile/v1:' succeeded -)" '1' 'baseline succeeded reconcile Start count'
assert_manager_starts_are_reconcile_operations "${baseline_journal}"

scenario 'B — Ingress hard failure is edge-only'
ingress_started_before="$(docker inspect --format '{{.State.StartedAt}}' "${ingress_container}")"
docker kill "${ingress_container}" >/dev/null
assert_public_unavailable /health
assert_container_running "${manager_container}" 'Manager container during Ingress failure'
assert_container_running "${runtime_container}" 'Runtime container during Ingress failure'
assert_cpa_pid_alive "${baseline_cpa_pid}"
scenario_b_status="$(wait_runtime_observation ready armed local 30)"
assert_equal "$(printf '%s' "${scenario_b_status}" | json_field runtimeIdentity)" "${baseline_identity}" 'Ingress failure RuntimeIdentity'
assert_equal "$(printf '%s' "${scenario_b_status}" | json_uint_field runtimeGeneration)" "${baseline_generation}" 'Ingress failure RuntimeGeneration'
docker start "${ingress_container}" >/dev/null
retry 30 container_running_by_id "${ingress_container}"
wait_public_success /health 60
wait_public_success /v1/models 60
sleep 6
assert_not_equal "$(docker inspect --format '{{.State.StartedAt}}' "${ingress_container}")" "${ingress_started_before}" 'Ingress process incarnation'
scenario_b_journal="$(snapshot_runtime_journal scenario-b-journal)"
assert_lifecycle_counts "${scenario_b_journal}" "${baseline_generation}" 1 0 0 'scenario B'
assert_equal "$(runtime_cpa_pid)" "${baseline_cpa_pid}" 'Ingress failure CPA PID'

scenario 'C — Manager hard failure preserves Gateway traffic'
manager_started_before="$(docker inspect --format '{{.State.StartedAt}}' "${manager_container}")"
docker kill "${manager_container}" >/dev/null
wait_public_success /v1/models 30
assert_container_running "${runtime_container}" 'Runtime container during Manager failure'
assert_cpa_pid_alive "${baseline_cpa_pid}"
scenario_c_status="$(wait_runtime_observation ready armed local 30)"
assert_equal "$(printf '%s' "${scenario_c_status}" | json_uint_field runtimeGeneration)" "${baseline_generation}" 'Manager failure RuntimeGeneration'
docker start "${manager_container}" >/dev/null
retry 30 container_running_by_id "${manager_container}"
wait_public_success /health 90
sleep 7
assert_not_equal "$(docker inspect --format '{{.State.StartedAt}}' "${manager_container}")" "${manager_started_before}" 'Manager process incarnation'
assert_equal "$(runtime_cpa_pid)" "${baseline_cpa_pid}" 'Manager failure CPA PID'
scenario_c_journal="$(snapshot_runtime_journal scenario-c-journal)"
assert_lifecycle_counts "${scenario_c_journal}" "${baseline_generation}" 1 0 0 'scenario C'

scenario 'D — Runtime hard failure creates a new generation'
runtime_started_before="$(docker inspect --format '{{.State.StartedAt}}' "${runtime_container}")"
docker kill "${runtime_container}" >/dev/null
wait_public_success /health 30
assert_public_unavailable /v1/models
docker pause "${manager_container}" >/dev/null
paused_manager="${manager_container}"
docker start "${runtime_container}" >/dev/null
retry 30 container_running_by_id "${runtime_container}"
scenario_d_inactive="$(wait_runtime_observation offline inactive local 60)"
scenario_d_generation="$(printf '%s' "${scenario_d_inactive}" | json_uint_field runtimeGeneration)"
assert_not_equal "${scenario_d_generation}" '0' 'scenario D RuntimeGeneration must be non-zero'
assert_not_equal "${scenario_d_generation}" "${baseline_generation}" 'Runtime restart must create a new generation'
assert_equal "$(printf '%s' "$scenario_d_inactive" | json_field activeGatewayArtifact.artifactId)" "$baseline_artifact_id" 'Runtime restart artifact identity'
scenario_d_pre_journal="$(snapshot_runtime_journal scenario-d-before-reconcile)"
assert_equal "$(journal_count "${scenario_d_pre_journal}" start "${baseline_generation}" 'runtime-reconcile/v1:' succeeded -)" '1' 'old-generation Start evidence after Runtime restart'
assert_equal "$(journal_count "${scenario_d_pre_journal}" start "${scenario_d_generation}" 'runtime-reconcile/v1:' - -)" '0' 'new generation before Manager reconcile'
docker unpause "${manager_container}" >/dev/null
paused_manager=""
scenario_d_ready="$(wait_runtime_observation ready armed local 120)"
assert_equal "$(printf '%s' "${scenario_d_ready}" | json_uint_field runtimeGeneration)" "${scenario_d_generation}" 'scenario D ready generation'
wait_public_success /v1/models 60
sleep 7
assert_not_equal "$(docker inspect --format '{{.State.StartedAt}}' "${runtime_container}")" "${runtime_started_before}" 'Runtime process incarnation'
scenario_d_cpa_pid="$(runtime_cpa_pid)"
scenario_d_journal="$(snapshot_runtime_journal scenario-d-journal)"
assert_lifecycle_counts "${scenario_d_journal}" "${scenario_d_generation}" 1 0 0 'scenario D'
assert_equal "$(journal_count "${scenario_d_journal}" start "${baseline_generation}" 'runtime-reconcile/v1:' succeeded -)" '1' 'scenario D durable old-generation history'
scenario_d_manager_database="$(snapshot_manager_database scenario-d-manager)"
read -r scenario_d_desired scenario_d_revision scenario_d_updated_at < <(desired_observation "${scenario_d_manager_database}")
assert_equal "${scenario_d_desired}" 'running' 'scenario D desired lifecycle'
assert_equal "${scenario_d_revision}" "${baseline_revision}" 'scenario D desired revision'
assert_equal "${scenario_d_updated_at}" "${baseline_updated_at}" 'scenario D desired timestamp'

scenario 'E — private Manager-to-Runtime network partition is observation-only'
runtime_network_output="$(
  docker inspect "${runtime_container}" | node -e '
    const containers = JSON.parse(require("node:fs").readFileSync(0, "utf8"));
    const entries = Object.entries(containers[0]?.NetworkSettings?.Networks ?? {});
    if (entries.length !== 1) process.exit(2);
    const [name, details] = entries[0];
    console.log(name);
    for (const alias of details.Aliases ?? []) console.log(alias);
  '
)"
network_name="$(printf '%s\n' "${runtime_network_output}" | sed -n '1p')"
test -n "${network_name}" || fail 'could not resolve the Runtime Compose network'
network_aliases=()
while IFS= read -r alias; do
  if [ -n "${alias}" ]; then network_aliases+=("${alias}"); fi
done < <(printf '%s\n' "${runtime_network_output}" | sed -n '2,$p')
if ! printf '%s\n' "${network_aliases[@]}" | grep -Fxq 'cpamp-runtime'; then
  fail 'Runtime Compose network is missing the cpamp-runtime service alias'
fi
docker network disconnect "${network_name}" "${runtime_container}"
disconnected_network="${network_name}"
disconnected_runtime="${runtime_container}"
wait_public_success /health 30
assert_public_unavailable /v1/models
scenario_e_status="$(wait_runtime_observation ready armed local 30)"
assert_equal "$(printf '%s' "${scenario_e_status}" | json_uint_field runtimeGeneration)" "${scenario_d_generation}" 'partition RuntimeGeneration'
assert_equal "$(runtime_cpa_pid)" "${scenario_d_cpa_pid}" 'partition CPA PID'
sleep 7
scenario_e_partition_journal="$(snapshot_runtime_journal scenario-e-partition-journal)"
assert_lifecycle_counts "${scenario_e_partition_journal}" "${scenario_d_generation}" 1 0 0 'scenario E partition'
network_connect_args=()
for alias in "${network_aliases[@]}"; do
  network_connect_args+=(--alias "${alias}")
done
docker network connect "${network_connect_args[@]}" "${network_name}" "${runtime_container}"
disconnected_network=""
disconnected_runtime=""
wait_public_success /v1/models 90
scenario_e_reconnected="$(wait_runtime_observation ready armed manager 90)"
assert_equal "$(printf '%s' "${scenario_e_reconnected}" | json_uint_field runtimeGeneration)" "${scenario_d_generation}" 'reconnected RuntimeGeneration'
assert_equal "$(runtime_cpa_pid)" "${scenario_d_cpa_pid}" 'reconnected CPA PID'
scenario_e_journal="$(snapshot_runtime_journal scenario-e-reconnected-journal)"
assert_lifecycle_counts "${scenario_e_journal}" "${scenario_d_generation}" 1 0 0 'scenario E reconnect'

scenario 'F — readiness failure is not liveness failure'
stopped_cpa_pid="${scenario_d_cpa_pid}"
"${compose[@]}" exec -T cpamp-runtime sh -ec 'kill -STOP "$1"' sh "${stopped_cpa_pid}"
scenario_f_starting="$(wait_runtime_observation starting armed local 30)"
assert_equal "$(printf '%s' "${scenario_f_starting}" | json_uint_field runtimeGeneration)" "${scenario_d_generation}" 'SIGSTOP RuntimeGeneration'
assert_cpa_pid_alive "${stopped_cpa_pid}"
wait_public_success /health 30
sleep 7
scenario_f_paused_journal="$(snapshot_runtime_journal scenario-f-paused-journal)"
assert_lifecycle_counts "${scenario_f_paused_journal}" "${scenario_d_generation}" 1 0 0 'scenario F SIGSTOP'
"${compose[@]}" exec -T cpamp-runtime sh -ec 'kill -CONT "$1"' sh "${stopped_cpa_pid}"
stopped_cpa_pid=""
scenario_f_ready="$(wait_runtime_observation ready armed local 60)"
assert_equal "$(printf '%s' "${scenario_f_ready}" | json_uint_field runtimeGeneration)" "${scenario_d_generation}" 'SIGCONT RuntimeGeneration'
assert_equal "$(runtime_cpa_pid)" "${scenario_d_cpa_pid}" 'SIGSTOP/SIGCONT CPA PID'
wait_public_success /v1/models 60
scenario_f_journal="$(snapshot_runtime_journal scenario-f-journal)"
assert_lifecycle_counts "${scenario_f_journal}" "${scenario_d_generation}" 1 0 0 'scenario F SIGCONT'

scenario 'G — confirmed CPA exit is recovered only by Supervisor'
scenario_g_old_pid="$(runtime_cpa_pid)"
"${compose[@]}" exec -T cpamp-runtime sh -ec 'kill -KILL "$1"' sh "${scenario_g_old_pid}"
scenario_g_ready="$(wait_runtime_observation ready armed local 120)"
assert_equal "$(printf '%s' "${scenario_g_ready}" | json_uint_field runtimeGeneration)" "${scenario_d_generation}" 'automatic recovery RuntimeGeneration'
assert_equal "$(printf '%s' "$scenario_g_ready" | json_field activeGatewayArtifact.artifactId)" "$baseline_artifact_id" 'same-binary automatic recovery artifact identity'
assert_equal "$(printf '%s' "${scenario_g_ready}" | json_field recovery.attemptsRemaining)" '2' 'automatic recovery remaining budget'
scenario_g_new_pid="$(runtime_cpa_pid)"
assert_not_equal "${scenario_g_new_pid}" "${scenario_g_old_pid}" 'automatic recovery must replace the CPA child'
sleep 7
scenario_g_journal="$(snapshot_runtime_journal scenario-g-journal)"
assert_lifecycle_counts "${scenario_g_journal}" "${scenario_d_generation}" 1 1 0 'scenario G'
assert_equal "$(journal_count "${scenario_g_journal}" auto_recovery "${scenario_d_generation}" 'auto-recovery-' succeeded -)" '1' 'scenario G succeeded auto_recovery evidence'
wait_public_success /v1/models 60

scenario 'H — bounded exhaustion creates a manual-intervention fence'
docker restart "${runtime_container}" >/dev/null
scenario_h_baseline="$(wait_runtime_observation ready armed local 120)"
scenario_h_generation="$(printf '%s' "${scenario_h_baseline}" | json_uint_field runtimeGeneration)"
assert_not_equal "${scenario_h_generation}" "${scenario_d_generation}" 'scenario H setup must use a fresh generation'
assert_equal "$(printf '%s' "${scenario_h_baseline}" | json_field recovery.attemptsRemaining)" '3' 'scenario H fresh recovery budget'
scenario_h_pid="$(runtime_cpa_pid)"
scenario_h_baseline_journal="$(snapshot_runtime_journal scenario-h-baseline-journal)"
assert_lifecycle_counts "${scenario_h_baseline_journal}" "${scenario_h_generation}" 1 1 0 'scenario H baseline'

"${compose[@]}" exec -T cpamp-runtime sh -ec '
  test -x "$1"
  test ! -e "$2"
  mv "$1" "$2"
' sh "${cpa_executable}" "${cpa_restore_path}"
executable_moved="true"
"${compose[@]}" exec -T cpamp-runtime sh -ec 'kill -KILL "$1"' sh "${scenario_h_pid}"
scenario_h_manual="$(wait_runtime_observation offline manual_intervention local 120)"
assert_equal "$(printf '%s' "${scenario_h_manual}" | json_uint_field runtimeGeneration)" "${scenario_h_generation}" 'manual-intervention RuntimeGeneration'
assert_equal "$(printf '%s' "${scenario_h_manual}" | json_field recovery.attemptsRemaining)" '0' 'manual-intervention recovery budget'
if runtime_cpa_pid >/dev/null 2>&1; then
  fail 'CPA child remained after automatic recovery exhaustion'
fi
scenario_h_exhausted_journal="$(snapshot_runtime_journal scenario-h-exhausted-journal)"
assert_lifecycle_counts "${scenario_h_exhausted_journal}" "${scenario_h_generation}" 1 4 0 'scenario H exhaustion'
assert_equal "$(journal_count "${scenario_h_exhausted_journal}" auto_recovery "${scenario_h_generation}" 'auto-recovery-' failed process_recovery_start_failed)" '3' 'scenario H bounded failed recovery attempts'

# Cross two Manager reconcile intervals while the executable is still absent.
sleep 12
scenario_h_fenced_journal="$(snapshot_runtime_journal scenario-h-fenced-journal)"
assert_lifecycle_counts "${scenario_h_fenced_journal}" "${scenario_h_generation}" 1 4 0 'scenario H manual fence'
scenario_h_manager_database="$(snapshot_manager_database scenario-h-manager)"
read -r scenario_h_desired scenario_h_revision scenario_h_updated_at < <(desired_observation "${scenario_h_manager_database}")
assert_equal "${scenario_h_desired}" 'running' 'scenario H desired lifecycle'
assert_equal "${scenario_h_revision}" "${baseline_revision}" 'scenario H desired revision'

"${compose[@]}" exec -T cpamp-runtime sh -ec '
  test -e "$2"
  test ! -e "$1"
  mv "$2" "$1"
  test -x "$1"
' sh "${cpa_executable}" "${cpa_restore_path}"
executable_moved="false"
sleep 12
scenario_h_restored="$(wait_runtime_observation offline manual_intervention local 10)"
assert_equal "$(printf '%s' "${scenario_h_restored}" | json_uint_field runtimeGeneration)" "${scenario_h_generation}" 'restored executable manual fence generation'
if runtime_cpa_pid >/dev/null 2>&1; then
  fail 'restoring the executable bypassed manual intervention'
fi
scenario_h_restored_journal="$(snapshot_runtime_journal scenario-h-restored-journal)"
assert_lifecycle_counts "${scenario_h_restored_journal}" "${scenario_h_generation}" 1 4 0 'scenario H restored executable'

# Hold Manager so the new Supervisor incarnation can be observed before the
# persisted desired=running state legitimately creates a generation-scoped Start.
docker pause "${manager_container}" >/dev/null
paused_manager="${manager_container}"
docker restart "${runtime_container}" >/dev/null
scenario_h_new_inactive="$(wait_runtime_observation offline inactive local 60)"
scenario_h_final_generation="$(printf '%s' "${scenario_h_new_inactive}" | json_uint_field runtimeGeneration)"
assert_not_equal "${scenario_h_final_generation}" "${scenario_h_generation}" 'manual-intervention recovery must use a new generation'
scenario_h_before_final_reconcile="$(snapshot_runtime_journal scenario-h-before-final-reconcile)"
assert_equal "$(journal_count "${scenario_h_before_final_reconcile}" auto_recovery "${scenario_h_generation}" 'auto-recovery-' failed process_recovery_start_failed)" '3' 'failed recovery history after Supervisor restart'
assert_equal "$(journal_count "${scenario_h_before_final_reconcile}" start "${scenario_h_final_generation}" 'runtime-reconcile/v1:' - -)" '0' 'final generation before Manager reconcile'
docker unpause "${manager_container}" >/dev/null
paused_manager=""
scenario_h_final_ready="$(wait_runtime_observation ready armed local 120)"
assert_equal "$(printf '%s' "${scenario_h_final_ready}" | json_uint_field runtimeGeneration)" "${scenario_h_final_generation}" 'final ready generation'
assert_equal "$(printf '%s' "$scenario_h_final_ready" | json_field activeGatewayArtifact.artifactId)" "$baseline_artifact_id" 'restored same-binary artifact identity'
wait_public_success /health 60
wait_public_success /v1/models 60
sleep 7

final_journal="$(snapshot_runtime_journal final-journal)"
assert_lifecycle_counts "${final_journal}" "${scenario_h_final_generation}" 1 4 0 'final stack'
assert_equal "$(journal_count "${final_journal}" start - 'runtime-reconcile/v1:' succeeded -)" '4' 'total generation-scoped Manager Start evidence'
assert_equal "$(journal_count "${final_journal}" auto_recovery "${scenario_d_generation}" 'auto-recovery-' succeeded -)" '1' 'successful recovery history'
assert_equal "$(journal_count "${final_journal}" auto_recovery "${scenario_h_generation}" 'auto-recovery-' failed process_recovery_start_failed)" '3' 'manual-intervention failure history'
assert_manager_starts_are_reconcile_operations "${final_journal}"
final_manager_database="$(snapshot_manager_database final-manager)"
read -r final_desired final_revision final_updated_at < <(desired_observation "${final_manager_database}")
assert_equal "${final_desired}" 'running' 'final desired lifecycle'
assert_equal "${final_revision}" "${baseline_revision}" 'final desired revision'
assert_equal "${final_updated_at}" "${baseline_updated_at}" 'final desired timestamp'

echo "Runtime 14 failure E2E passed: A-H preserved authority boundaries, generations, exact-child evidence, durable journal history, and the manual-intervention fence"
