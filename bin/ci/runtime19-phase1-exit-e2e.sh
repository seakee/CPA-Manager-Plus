#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# The Dockerfiles fail closed on identity; bind a local build to this checkout.
export VERSION="${VERSION:-dev}"
export SOURCE_COMMIT="${SOURCE_COMMIT:-$(git -C "${repo_root}" rev-parse HEAD)}"
project="${CPAMP_RUNTIME19_PROJECT:-${CPAMP_SMOKE_PROJECT:-cpamp-runtime19-${RANDOM}-$$}}"
public_port="${CPAMP_RUNTIME19_PORT:-${CPAMP_SMOKE_PORT:-${CPAMP_PUBLIC_PORT:-32317}}}"
skip_build="${CPAMP_RUNTIME19_SKIP_BUILD:-${CPAMP_SMOKE_SKIP_BUILD:-false}}"
initializer_name="${project}-cpamp-manager-init"
snapshot_root="$(mktemp -d "${TMPDIR:-/tmp}/cpamp-runtime19-exit.XXXXXX")"
chmod 700 "${snapshot_root}"
up_args=(-d)
if [ "${skip_build}" = "true" ]; then
  up_args+=(--no-build)
fi
compose_override="${CPAMP_SMOKE_COMPOSE_OVERRIDE:-}"
compose=(docker compose --project-directory "${repo_root}" -f "${repo_root}/docker-compose.yml")
if [ -n "${compose_override}" ]; then
  if [ ! -f "${compose_override}" ]; then
    echo "CPAMP_SMOKE_COMPOSE_OVERRIDE does not exist or is not a regular file: ${compose_override}" >&2
    rm -rf "${snapshot_root}"
    exit 2
  fi
  compose_override="$(cd "$(dirname "${compose_override}")" && pwd)/$(basename "${compose_override}")"
  export CPAMP_SMOKE_COMPOSE_OVERRIDE="${compose_override}"
  compose+=(-f "${compose_override}")
fi
compose+=(-p "${project}")

paused_manager_pid=""
bundled_hold_path="/usr/local/bin/cli-proxy-api.runtime19-bundled-hold"
stage_restore_moved="false"

cleanup() {
  set +e
  docker rm -f "${initializer_name}" >/dev/null 2>&1 || true
  if [ -n "${paused_manager_pid}" ]; then
    "${compose[@]}" exec -T cpa-manager-plus sh -ec 'kill -CONT "$1"' sh "${paused_manager_pid}" >/dev/null 2>&1 || true
  fi
  "${compose[@]}" exec -T cpa-manager-plus sh -ec '
    if [ -e "$1" ] && [ ! -e /usr/local/bin/cli-proxy-api ]; then
      mv "$1" /usr/local/bin/cli-proxy-api
    fi
  ' sh "${bundled_hold_path}" >/dev/null 2>&1 || true
  if [ "${stage_restore_moved}" = "true" ]; then
    "${compose[@]}" exec -T cpa-manager-plus sh -ec '
      rt_root="/data/runtime"
      [ ! -d /data/runtime ] && [ -d /runtime ] && rt_root="/runtime"
      stage_bin="${rt_root}/supervisor/artifacts/cpa/8.0.55/cli-proxy-api"
      if [ -e "${stage_bin}.runtime19-restore" ] && [ ! -e "${stage_bin}" ]; then
        chmod 710 "$(dirname "${stage_bin}")"
        mv "${stage_bin}.runtime19-restore" "${stage_bin}"
        chmod 510 "$(dirname "${stage_bin}")"
      fi
    ' >/dev/null 2>&1 || true
  fi
  "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
  rm -rf "${snapshot_root}"
}
trap cleanup EXIT HUP INT TERM

fail() {
  echo "Runtime19 Phase1 exit E2E: $*" >&2
  exit 1
}

report_unexpected_failure() {
  echo "Runtime19 Phase1 exit E2E: unexpected command failure at line $1" >&2
}
trap 'report_unexpected_failure "${LINENO}"' ERR

scenario() {
  echo "Runtime19 scenario $1"
}

retry() {
  local attempts="$1"
  shift
  local count=0
  until "$@"; do
    count=$((count + 1))
    if [ "${count}" -ge "${attempts}" ]; then return 1; fi
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
    const match = input.match(new RegExp("\"" + escaped + "\"\\s*:\\s*(\\d+)"));
    if (!match) process.exit(2);
    process.stdout.write(match[1]);
  ' "${field}"
}

container_id() {
  local service="$1"
  local id
  id="$("${compose[@]}" ps --all -q "${service}")"
  test -n "${id}"
  printf '%s' "${id}"
}

runtime_root() {
  "${compose[@]}" exec -T cpa-manager-plus sh -ec 'if [ -d /data/runtime ]; then printf /data/runtime; else printf /runtime; fi'
}

runtime_token_file() {
  "${compose[@]}" exec -T cpa-manager-plus sh -ec '
    for c in /data/runtime-secret/token /run/cpamp/runtime-secret/token /data/.cpamp-runtime-secret/token /data/runtime/supervisor/token; do
      if [ -e "$c" ]; then printf "%s" "$c"; exit 0; fi
    done
    printf /data/runtime-secret/token
  '
}

service_pid() {
  local binary="$1"
  "${compose[@]}" exec -T cpa-manager-plus sh -ec '
    pidof "$1" | awk "{print \$1}"
  ' sh "${binary}"
}

runtime_get_from_manager() {
  local request_path="$1"
  "${compose[@]}" exec -T --user 10002:10002 cpa-manager-plus sh -ec '
    for c in /data/runtime-secret/token /run/cpamp/runtime-secret/token /data/.cpamp-runtime-secret/token /data/runtime/supervisor/token; do
      if [ -e "$c" ]; then token_file="$c"; break; fi
    done
    token="$(cat "${token_file}")"
    wget -qO- -T 5 \
      --header="Authorization: Bearer ${token}" \
      --header="X-CPAMP-Runtime-Features: active-gateway-artifact-v1" \
      "http://127.0.0.1:9081${1}"
  ' sh "${request_path}"
}

runtime_get_local() {
  local request_path="$1"
  "${compose[@]}" exec -T cpa-manager-plus sh -ec '
    for c in /data/runtime-secret/token /run/cpamp/runtime-secret/token /data/.cpamp-runtime-secret/token /data/runtime/supervisor/token; do
      if [ -e "$c" ]; then token_file="$c"; break; fi
    done
    token="$(cat "${token_file}")"
    wget -qO- -T 5 \
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

runtime_activate() {
  local payload="$1"
  "${compose[@]}" exec -T cpa-manager-plus sh -ec '
    for c in /data/runtime-secret/token /run/cpamp/runtime-secret/token /data/.cpamp-runtime-secret/token /data/runtime/supervisor/token; do
      if [ -e "$c" ]; then token_file="$c"; break; fi
    done
    token="$(cat "${token_file}")"
    wget -qO- -T 170 \
      --header="Authorization: Bearer ${token}" \
      --header="Content-Type: application/json" \
      --post-data="${1}" \
      http://127.0.0.1:9081/v1/runtime/operations/activate-update
  ' sh "${payload}"
}
activate() {
  local operation_id="$1"
  local generation="$2"
  local active_artifact="$3"
  local version="$4"
  local payload
  payload="$(printf '{"operationId":"%s","expectedRuntimeIdentity":"embedded-default","expectedRuntimeGeneration":%s,"expectedActiveArtifactId":"%s","targetVersion":"%s"}' \
    "${operation_id}" "${generation}" "${active_artifact}" "${version}")"
  runtime_activate "${payload}"
}

wait_runtime_observation() {
  local expected_state="$1"
  local expected_recovery="$2"
  local expected_artifact="$3"
  local source="${4:-manager}"
  local attempts="${5:-120}"
  local count=0
  while [ "${count}" -lt "${attempts}" ]; do
    local status state recovery artifact
    if status="$(runtime_get "${source}" /v1/runtime/status 2>/dev/null)"; then
      state="$(printf '%s' "${status}" | json_field state 2>/dev/null || true)"
      recovery="$(printf '%s' "${status}" | json_field recovery.state 2>/dev/null || true)"
      artifact="$(printf '%s' "${status}" | json_field activeGatewayArtifact.artifactId 2>/dev/null || true)"
      if [ "${state}" = "${expected_state}" ] &&
        [ "${recovery}" = "${expected_recovery}" ] &&
        { [ "${expected_artifact}" = "-" ] || [ "${artifact}" = "${expected_artifact}" ]; }; then
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
  "${compose[@]}" exec -T cpa-manager-plus sh -ec '
    for process_dir in /proc/[0-9]*; do
      if [ -r "${process_dir}/comm" ] && [ "$(cat "${process_dir}/comm")" = "cli-proxy-api" ]; then
        printf "%s" "${process_dir#/proc/}"
        exit 0
      fi
    done
    exit 1
  '
}

assert_cpa_pid_alive() {
  local expected_pid="$1"
  "${compose[@]}" exec -T cpa-manager-plus sh -ec '
    test -r "/proc/${1}/comm"
    test "$(cat "/proc/${1}/comm")" = "cli-proxy-api"
  ' sh "${expected_pid}"
}

assert_no_cpa() {
  if runtime_cpa_pid >/dev/null 2>&1; then
    fail "a CPA child exists while the Runtime must be offline"
  fi
}

assert_cpa_identity() {
  local pid="$1"
  local actual_uid actual_gid actual_groups expected_uid expected_gid
  actual_uid="$("${compose[@]}" exec -T cpa-manager-plus awk '$1 == "Uid:" { print $2; exit }' "/proc/${pid}/status")"
  actual_gid="$("${compose[@]}" exec -T cpa-manager-plus awk '$1 == "Gid:" { print $2; exit }' "/proc/${pid}/status")"
  actual_groups="$("${compose[@]}" exec -T cpa-manager-plus awk '$1 == "Groups:" { $1 = ""; sub(/^ +/, ""); print; exit }' "/proc/${pid}/status")"
  expected_uid="$("${compose[@]}" exec -T cpa-manager-plus sh -ec 'printf %s "${CPAMP_RUNTIME_CPA_UID:-10001}"')"
  expected_gid="$("${compose[@]}" exec -T cpa-manager-plus sh -ec 'printf %s "${CPAMP_RUNTIME_CPA_GID:-10001}"')"
  assert_equal "${actual_uid}" "${expected_uid}" "CPA PID ${pid} UID"
  assert_equal "${actual_gid}" "${expected_gid}" "CPA PID ${pid} GID"
  assert_equal "${actual_groups}" "${expected_gid}" "CPA PID ${pid} supplementary groups"
  [ "${actual_uid}" != 0 ] && [ "${actual_gid}" != 0 ] || fail "CPA PID ${pid} retained root identity"
}

assert_gateway_writable() {
  local pid="$1"
  local uid gid
  uid="$("${compose[@]}" exec -T cpa-manager-plus awk '$1 == "Uid:" { print $2; exit }' "/proc/${pid}/status")"
  gid="$("${compose[@]}" exec -T cpa-manager-plus awk '$1 == "Gid:" { print $2; exit }' "/proc/${pid}/status")"
  "${compose[@]}" exec -T --user "${uid}:${gid}" cpa-manager-plus sh -ec '
    rt_root="/data/runtime"
    [ ! -d /data/runtime ] && [ -d /runtime ] && rt_root="/runtime"
    test -r "${rt_root}/gateway/config.yaml"
    test -w "${rt_root}/gateway"
    marker="${rt_root}/gateway/.runtime19-cpa-write"
    printf runtime19 >"${marker}"
    test "$(cat "${marker}")" = runtime19
    rm "${marker}"
  '
}

assert_secret_and_private_state_denied() {
  local pid="$1"
  local uid gid rt_root token_file env_dump
  uid="$("${compose[@]}" exec -T cpa-manager-plus awk '$1 == "Uid:" { print $2; exit }' "/proc/${pid}/status")"
  gid="$("${compose[@]}" exec -T cpa-manager-plus awk '$1 == "Gid:" { print $2; exit }' "/proc/${pid}/status")"
  rt_root="$(runtime_root)"
  token_file="$(runtime_token_file)"
  if "${compose[@]}" exec -T --user "${uid}:${gid}" cpa-manager-plus cat "${token_file}" >/dev/null 2>&1; then
    fail "CPA identity can read the Runtime transport secret"
  fi
  if "${compose[@]}" exec -T --user "${uid}:${gid}" cpa-manager-plus ls "${rt_root}/supervisor" >/dev/null 2>&1; then
    fail "CPA identity can enumerate Supervisor private state"
  fi
  if "${compose[@]}" exec -T --user "${uid}:${gid}" cpa-manager-plus cat "${rt_root}/supervisor/operations.sqlite" >/dev/null 2>&1; then
    fail "CPA identity can read the Supervisor journal"
  fi
  if "${compose[@]}" exec -T --user "${uid}:${gid}" cpa-manager-plus cat "${rt_root}/supervisor/active/cpa/selection.json" >/dev/null 2>&1; then
    fail "CPA identity can read active selection"
  fi
  if "${compose[@]}" exec -T --user "${uid}:${gid}" cpa-manager-plus cat "${rt_root}/supervisor/artifacts/cpa/.runtime19-private/marker" >/dev/null 2>&1; then
    fail "CPA identity can read unrelated Supervisor artifact state"
  fi
  if "${compose[@]}" exec -T --user "${uid}:${gid}" cpa-manager-plus sh -ec "printf x >>'${rt_root}/supervisor/operations.sqlite'" >/dev/null 2>&1; then
    fail "CPA identity can mutate Supervisor private state"
  fi
  env_dump="$("${compose[@]}" exec -T --user "${uid}:${gid}" cpa-manager-plus sh -ec 'tr "\000" "\n" </proc/"$1"/environ' sh "${pid}")"
  if printf '%s\n' "${env_dump}" | grep -Eq '^(CPAMP_RUNTIME_|CPAMP_CPA_EXECUTABLE=|CPAMP_CPA_ARTIFACT_MANIFEST=)'; then
    fail "CPA environment contains Supervisor-private Runtime configuration"
  fi
}

assert_mount_isolation() {
  local rt_root token_file token_dir
  rt_root="$(runtime_root)"
  token_file="$(runtime_token_file)"
  token_dir="${token_file%/*}"
  "${compose[@]}" exec -T --user 10002:10002 cpa-manager-plus sh -ec '
    test -d /data
    test ! -w "$1/supervisor"
    test -r "$2"
    ! touch "$3/.runtime19-write-probe"
  ' sh "${rt_root}" "${token_file}" "${token_dir}" >/dev/null 2>&1 || fail "Manager data, Runtime, or read-only token boundary changed"
  "${compose[@]}" exec -T --user 10003:10003 cpa-manager-plus sh -ec '
    ! ls /data >/dev/null 2>&1
    ! test -r "$2"
    ! ls "$1/supervisor" >/dev/null 2>&1
  ' sh "${rt_root}" "${token_file}" || fail "Ingress gained Manager, Runtime, or token storage"
  "${compose[@]}" exec -T cpa-manager-plus sh -ec '
    test "$(awk '\''$1 == "Uid:" { print $2; exit }'\'' /proc/1/status)" = 0
    test -r "$2"
    test -w "$1/supervisor"
  ' sh "${rt_root}" "${token_file}" || fail "Runtime Supervisor is not the privileged Runtime owner"
}

assert_execution_corridor() {
  local pid="$1"
  local version="$2"
  local uid gid rt_root executable
  uid="$("${compose[@]}" exec -T cpa-manager-plus awk '$1 == "Uid:" { print $2; exit }' "/proc/${pid}/status")"
  gid="$("${compose[@]}" exec -T cpa-manager-plus awk '$1 == "Gid:" { print $2; exit }' "/proc/${pid}/status")"
  rt_root="$(runtime_root)"
  executable="${rt_root}/supervisor/artifacts/cpa/${version}/cli-proxy-api"
  "${compose[@]}" exec -T --user "${uid}:${gid}" cpa-manager-plus test -r "${executable}" ||
    fail "CPA cannot read finalized ${version} executable"
  "${compose[@]}" exec -T --user "${uid}:${gid}" cpa-manager-plus test -x "${executable}" ||
    fail "CPA cannot execute finalized ${version} executable"
  if "${compose[@]}" exec -T --user "${uid}:${gid}" cpa-manager-plus ls "${rt_root}/supervisor/artifacts/cpa" >/dev/null 2>&1; then
    fail "artifact execution corridor permits store enumeration"
  fi
}

post_runtime_json() {
  local cid="$1"
  local endpoint="$2"
  local body_file="${snapshot_root}/post-body.json"
  local resp rc=0
  (
    umask 077
    cat >"${body_file}"
  )
  resp="$(python3 "${repo_root}/bin/ci/runtime-v8-upload-stage.py" post-json \
    "${cid}" "${endpoint}" --body-file "${body_file}")" || rc=$?
  rm -f "${body_file}"
  [ "${rc}" -eq 0 ] || return "${rc}"
  printf '%s' "${resp}"
}

resolve_public_origin() {
  local bound_port="${public_port##*:}"
  if [ "${bound_port}" = "0" ]; then
    bound_port="$("${compose[@]}" port cpa-manager-plus 18317)"
    bound_port="${bound_port##*:}"
  fi
  public_origin="http://127.0.0.1:${bound_port}"
}

cpa_exe_path() {
  local pid="$1"
  local uid gid
  uid="$("${compose[@]}" exec -T cpa-manager-plus awk '$1 == "Uid:" { print $2; exit }' "/proc/${pid}/status")"
  gid="$("${compose[@]}" exec -T cpa-manager-plus awk '$1 == "Gid:" { print $2; exit }' "/proc/${pid}/status")"
  "${compose[@]}" exec -T --user "${uid}:${gid}" cpa-manager-plus readlink "/proc/${pid}/exe"
}

wait_offline_new_generation() {
  local previous_generation="$1"
  local attempts="${2:-60}"
  local count=0
  while [ "${count}" -lt "${attempts}" ]; do
    local status state generation
    if status="$(runtime_get local /v1/runtime/status 2>/dev/null)"; then
      state="$(printf '%s' "${status}" | json_field state 2>/dev/null || true)"
      generation="$(printf '%s' "${status}" | json_uint_field runtimeGeneration 2>/dev/null || true)"
      if [ "${state}" = "offline" ] && [ -n "${generation}" ] && [ "${generation}" != "${previous_generation}" ]; then
        printf '%s' "${status}"
        return 0
      fi
    fi
    count=$((count + 1))
    sleep 1
  done
  return 1
}

# Negative rollback fault-injection only: stages a non-CPA system binary under a
# non-v8 legacy version directory strictly to test ActivateUpdate failure and
# rollback to the genuine pinned CPA 8.0.55 stage.
seed_negative_fault_stage() {
  local version="$1"
  local source="$2"
  "${compose[@]}" exec -T cpa-manager-plus sh -ec '
    version="$1"
    source="$2"
    rt_root="/data/runtime"
    [ ! -d /data/runtime ] && [ -d /runtime ] && rt_root="/runtime"
    cpa_gid="${CPAMP_RUNTIME_CPA_GID:-10001}"
    directory="${rt_root}/supervisor/artifacts/cpa/${version}"
    mkdir -p "${directory}"
    chmod 700 "${directory}"
    cp "${source}" "${directory}/cli-proxy-api"
    chmod 755 "${directory}/cli-proxy-api"
    digest="$(sha256sum "${directory}/cli-proxy-api")"
    digest="${digest%% *}"
    printf "{\"schemaVersion\":1,\"engine\":\"cpa\",\"version\":\"%s\",\"artifactId\":\"sha256:%s\",\"sourceArchiveDigest\":\"sha256:%064d\"}\n" \
      "${version}" "${digest}" 0 >"${directory}/artifact.json"
    chmod 444 "${directory}/artifact.json"
    chown root:"${cpa_gid}" "${directory}"
    chmod 510 "${directory}"
    printf "sha256:%s" "${digest}"
  ' sh "${version}" "${source}"
}

assert_selection() {
  local expected_version="$1"
  local expected_artifact="$2"
  local selected
  selected="$("${compose[@]}" exec -T cpa-manager-plus cat "$(runtime_root)/supervisor/active/cpa/selection.json")"
  assert_equal "$(printf '%s' "${selected}" | json_field schemaVersion)" 1 "selection schema"
  assert_equal "$(printf '%s' "${selected}" | json_field engine)" cpa "selection engine"
  assert_equal "$(printf '%s' "${selected}" | json_field version)" "${expected_version}" "selection version"
  assert_equal "$(printf '%s' "${selected}" | json_field artifactId)" "${expected_artifact}" "selection artifact"
  if printf '%s' "${selected}" | grep -Eq '"(path|url|command)"'; then
    fail "selection persisted forbidden execution authority"
  fi
}

wait_public_health() {
  local attempts="${1:-90}"
  retry "${attempts}" curl --fail --silent --show-error --connect-timeout 1 --max-time 5 \
    "${public_origin}/health" >/dev/null
}

public_gateway_signature() {
  local body status digest bytes
  body="$(mktemp "${snapshot_root}/public-gateway.XXXXXX")"
  if ! status="$(curl --silent --show-error --connect-timeout 1 --max-time 8 \
    --header 'User-Agent: runtime19-gateway-probe' \
    --output "${body}" --write-out '%{http_code}' \
    "${public_origin}/v1/models")"; then
    rm -f "${body}"
    return 1
  fi
  [ "${status}" != 000 ] || { rm -f "${body}"; return 1; }
  digest="$(node -e '
    const fs = require("node:fs");
    const crypto = require("node:crypto");
    process.stdout.write(crypto.createHash("sha256").update(fs.readFileSync(process.argv[1])).digest("hex"));
  ' "${body}")"
  bytes="$(wc -c <"${body}" | tr -d ' ')"
  rm -f "${body}"
  printf '%s|sha256:%s|%s' "${status}" "${digest}" "${bytes}"
}

private_gateway_signature() {
  "${compose[@]}" exec -T cpa-manager-plus sh -ec '
    body="$(mktemp)"
    headers="$(mktemp)"
    cleanup_probe() { rm -f "${body}" "${headers}"; }
    trap cleanup_probe EXIT
    wget -S -O "${body}" -T 8 \
      --header="User-Agent: runtime19-gateway-probe" \
      http://127.0.0.1:8317/v1/models 2>"${headers}" || true
    status="$(awk '\''$1 ~ /^HTTP\// { code=$2 } END { print code }'\'' "${headers}")"
    test -n "${status}"
    test -f "${body}"
    digest="$(sha256sum "${body}")"
    digest="${digest%% *}"
    bytes="$(wc -c <"${body}" | tr -d " ")"
    printf "%s|sha256:%s|%s" "${status}" "${digest}" "${bytes}"
  '
}

wait_public_gateway_signature() {
  local expected="$1"
  local attempts="${2:-90}"
  local count=0
  while [ "${count}" -lt "${attempts}" ]; do
    local actual
    if actual="$(public_gateway_signature 2>/dev/null)" && [ "${actual}" = "${expected}" ]; then
      printf '%s' "${actual}"
      return 0
    fi
    count=$((count + 1))
    sleep 1
  done
  return 1
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
  base="$(basename "${source}")"
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
  copy_sqlite_snapshot cpa-manager-plus "$(runtime_root)/supervisor/operations.sqlite" "$1"
}

snapshot_manager_database() {
  copy_sqlite_snapshot cpa-manager-plus /data/usage.sqlite "$1"
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
    if not operation_id.startswith("runtime-reconcile/v2:"):
        raise SystemExit(f"unexpected Start operation ID: {operation_id!r}")
    if state != "succeeded":
        raise SystemExit(f"Manager Start is not succeeded: {(operation_id, state)!r}")
PY
}

runtime_secret_digest() {
  "${compose[@]}" exec -T cpa-manager-plus sha256sum "$(runtime_token_file)" | awk '{print $1}'
}

gateway_config_digest() {
  "${compose[@]}" exec -T cpa-manager-plus sha256sum "$(runtime_root)/gateway/config.yaml" | awk '{print $1}'
}

cd "${repo_root}"
CPAMP_PUBLIC_PORT=18317 "${compose[@]}" config --format json | node bin/ci/validate-runtime12-compose.mjs
docker compose -f docker-compose.manager.yml config >/dev/null
export CPAMP_PUBLIC_PORT="${public_port}"

# The only volume reset in Runtime19 is this initial fresh-install boundary.
"${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
if [ "${skip_build}" != true ]; then
  "${compose[@]}" build
fi

scenario 'A — CPA v8.0.55 Manager lifecycle baseline'
# Runtime19 exercises persisted Runtime/Manager lifecycle semantics on the
# default CPA v8.0.55 full bundle.
"${repo_root}/bin/ci/runtime-lifecycle-manager-baseline.sh" "${project}" "${initializer_name}"
"${compose[@]}" up "${up_args[@]}"
resolve_public_origin
wait_public_health 120
status_a="$(wait_runtime_observation ready armed - manager 120)"
runtime_identity="$(printf '%s' "${status_a}" | json_field runtimeIdentity)"
generation_a="$(printf '%s' "${status_a}" | json_uint_field runtimeGeneration)"
artifact_a="$(printf '%s' "${status_a}" | json_field activeGatewayArtifact.artifactId)"
attempts_a="$(printf '%s' "${status_a}" | json_field recovery.attemptsRemaining)"
assert_equal "${runtime_identity}" embedded-default "baseline Runtime identity"
assert_not_equal "${generation_a}" 0 "baseline RuntimeGeneration"
assert_equal "${attempts_a}" 3 "baseline recovery budget"
assert_equal "$(printf '%s' "${status_a}" | json_field activeGatewayArtifact.version)" "8.0.55" "baseline trusted artifact version"
assert_equal "$(printf '%s' "${status_a}" | json_field cpaObservedVersion)" "8.0.55" "baseline observed CPA version"
[[ "${artifact_a}" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "bundled A artifact is not canonical: ${artifact_a}"
bundled_artifact="sha256:$("${compose[@]}" exec -T cpa-manager-plus sha256sum /usr/local/bin/cli-proxy-api | awk '{print $1}')"
assert_equal "${artifact_a}" "${bundled_artifact}" "baseline bundled A exact artifact"
manifest_json="$("${compose[@]}" exec -T cpa-manager-plus cat /usr/local/share/cpamp/runtime/cpa-artifact.json)"
assert_equal "$(printf '%s' "${manifest_json}" | json_field version)" "8.0.55" "pinned CPA manifest version"
assert_equal "$(printf '%s' "${manifest_json}" | json_field artifactId)" "${artifact_a}" "pinned CPA manifest artifactId"
pid_a="$(runtime_cpa_pid)"
assert_cpa_pid_alive "${pid_a}"
assert_cpa_identity "${pid_a}"
assert_equal "$(cpa_exe_path "${pid_a}")" "/usr/local/bin/cli-proxy-api" "baseline CPA executable path"

rt_root="$(runtime_root)"
"${compose[@]}" exec -T cpa-manager-plus sh -ec '
  test -f "$1/.cpamp-v8-bootstrap"
  test "$(stat -c %u:%g:%a "$1/.cpamp-v8-bootstrap")" = "0:0:444"
  grep -q "^config-version: 8$" "$1/gateway/config.yaml"
  mkdir -p "$1/supervisor/artifacts/cpa/.runtime19-private"
  chmod 700 "$1/supervisor/artifacts/cpa/.runtime19-private"
  printf private >"$1/supervisor/artifacts/cpa/.runtime19-private/marker"
  chmod 600 "$1/supervisor/artifacts/cpa/.runtime19-private/marker"
  test ! -e "$1/supervisor/active/cpa/selection.json"
' sh "${rt_root}"
assert_gateway_writable "${pid_a}"
assert_secret_and_private_state_denied "${pid_a}"
assert_mount_isolation

private_gateway_a="$(private_gateway_signature)"
public_gateway_a="$(wait_public_gateway_signature "${private_gateway_a}" 120)"
assert_equal "${public_gateway_a}" "${private_gateway_a}" "baseline public/private Gateway semantics"
config_digest_a="$(gateway_config_digest)"
secret_digest_a="$(runtime_secret_digest)"
manager_a="$(snapshot_manager_database scenario-a-manager)"
read -r desired_a revision_a updated_at_a < <(desired_observation "${manager_a}")
assert_equal "${desired_a}" running "fresh desired lifecycle"
journal_a="$(snapshot_runtime_journal scenario-a-journal)"
assert_equal "$(journal_count "${journal_a}" start "${generation_a}" 'runtime-reconcile/v2:' succeeded -)" 1 "fresh reconcile Start evidence"
assert_equal "$(journal_count "${journal_a}" auto_recovery - - - -)" 0 "fresh auto-recovery evidence"
assert_equal "$(journal_count "${journal_a}" activate_update - - - -)" 0 "fresh activation evidence"
assert_manager_starts_are_reconcile_operations "${journal_a}"

scenario 'B — Manager down / real Gateway data-plane independence'
paused_manager_pid="$(service_pid cpa-manager-plus)"
"${compose[@]}" exec -T cpa-manager-plus sh -ec 'kill -STOP "$1"' sh "${paused_manager_pid}"
assert_equal "$("${compose[@]}" exec -T cpa-manager-plus awk '$1 == "State:" { print $2; exit }' "/proc/${paused_manager_pid}/status")" T "Manager stopped state"
private_gateway_down="$(private_gateway_signature)"
public_gateway_down="$(wait_public_gateway_signature "${private_gateway_a}" 30)"
assert_equal "${private_gateway_down}" "${private_gateway_a}" "private CPA route while Manager is down"
assert_equal "${public_gateway_down}" "${private_gateway_down}" "public Ingress route while Manager is down"
assert_cpa_pid_alive "${pid_a}"
status_manager_down="$(wait_runtime_observation ready armed "${artifact_a}" local 30)"
assert_equal "$(printf '%s' "${status_manager_down}" | json_uint_field runtimeGeneration)" "${generation_a}" "Manager-down RuntimeGeneration"
assert_equal "$(printf '%s' "${status_manager_down}" | json_field activeGatewayArtifact.artifactId)" "${artifact_a}" "Manager-down active artifact"
journal_manager_down="$(snapshot_runtime_journal scenario-b-manager-down-journal)"
assert_equal "$(journal_count "${journal_manager_down}" start - - - -)" 1 "Manager-down Start count"
assert_equal "$(journal_count "${journal_manager_down}" auto_recovery - - - -)" 0 "Manager-down recovery count"
assert_equal "$(journal_count "${journal_manager_down}" stop - - - -)" 0 "Manager-down Stop count"
assert_equal "$(journal_count "${journal_manager_down}" restart - - - -)" 0 "Manager-down Restart count"

"${compose[@]}" exec -T cpa-manager-plus sh -ec 'kill -CONT "$1"' sh "${paused_manager_pid}"
paused_manager_pid=""
wait_public_health 90
wait_runtime_observation ready armed "${artifact_a}" manager 60 >/dev/null
sleep 7
assert_equal "$(runtime_cpa_pid)" "${pid_a}" "Manager restart healthy CPA PID"
journal_manager_restored="$(snapshot_runtime_journal scenario-b-manager-restored-journal)"
assert_equal "$(journal_count "${journal_manager_restored}" start - - - -)" 1 "Manager restoration Start count"
assert_equal "$(journal_count "${journal_manager_restored}" stop - - - -)" 0 "Manager restoration Stop count"
assert_equal "$(journal_count "${journal_manager_restored}" restart - - - -)" 0 "Manager restoration Restart count"
manager_restored="$(snapshot_manager_database scenario-b-manager-restored)"
read -r desired_restored revision_restored updated_at_restored < <(desired_observation "${manager_restored}")
assert_equal "${desired_restored}" running "Manager restoration desired lifecycle"
assert_equal "${revision_restored}" "${revision_a}" "Manager restoration desired revision"
assert_equal "${updated_at_restored}" "${updated_at_a}" "Manager restoration desired timestamp"

scenario 'C — publish and select finalized 8.0.55 stage via real artifact upload API'
# Positive A -> B activate-update requires two distinct valid CPA binaries
# (activate_update.go enforces candidate.ArtifactID != prior.ArtifactID). Under
# the single pinned CPA 8.0.55 binary without downloading or forging binaries,
# verify pre-stage 409 target_stage_unavailable, publish/commit finalized stage
# 8.0.55 + selection.json via PUT /v1/runtime/artifact/upload, verify finalized
# stage execution overrides the restored bundled binary, and verify same-pin
# activate-update returns 409 operation_state_conflict.
cid_c="$(container_id cpa-manager-plus)"
missing_stage_c="$(
  printf '{"operationId":"runtime19-missing-stage","expectedRuntimeIdentity":"embedded-default","expectedRuntimeGeneration":%s,"expectedActiveArtifactId":"%s","targetVersion":"8.0.55"}' \
    "${generation_a}" "${artifact_a}" |
    post_runtime_json "${cid_c}" "/v1/runtime/operations/activate-update"
)"
assert_equal "$(printf '%s' "${missing_stage_c}" | json_field status)" 409 "pre-stage activate-update HTTP status"
assert_equal "$(printf '%s' "${missing_stage_c}" | json_field body.error.code)" target_stage_unavailable "pre-stage activate-update error code"
assert_equal "$(runtime_cpa_pid)" "${pid_a}" "pre-stage 409 preserved bundled child A"

paused_manager_pid="$(service_pid cpa-manager-plus)"
"${compose[@]}" exec -T cpa-manager-plus sh -ec '
  kill -STOP "$1"
  test -x /usr/local/bin/cli-proxy-api
  test ! -e "$2"
  mv /usr/local/bin/cli-proxy-api "$2"
  kill -TERM $(pidof cpamp-runtime-supervisor)
  kill -KILL $(pidof cli-proxy-api) 2>/dev/null || true
' sh "${paused_manager_pid}" "${bundled_hold_path}"
status_stage_offline="$(wait_offline_new_generation "${generation_a}" 60)"
generation_stage="$(printf '%s' "${status_stage_offline}" | json_uint_field runtimeGeneration)"
assert_not_equal "${generation_stage}" "${generation_a}" "artifact acquisition generation"
uploaded_artifact_c="$(python3 "${repo_root}/bin/ci/runtime-v8-upload-stage.py" upload-pinned \
  "${cid_c}" "${bundled_hold_path}" "${rt_root}")"
assert_equal "${uploaded_artifact_c}" "${artifact_a}" "uploaded finalized 8.0.55 artifact digest"
"${compose[@]}" exec -T cpa-manager-plus sh -ec '
  mv "$1" /usr/local/bin/cli-proxy-api
  test -x /usr/local/bin/cli-proxy-api
  kill -CONT "$2"
' sh "${bundled_hold_path}" "${paused_manager_pid}"
paused_manager_pid=""

status_b="$(wait_runtime_observation ready armed "${artifact_a}" manager 120)"
assert_equal "$(printf '%s' "${status_b}" | json_uint_field runtimeGeneration)" "${generation_stage}" "selected 8.0.55 RuntimeGeneration"
assert_equal "$(printf '%s' "${status_b}" | json_field activeGatewayArtifact.version)" "8.0.55" "selected 8.0.55 artifact version"
assert_equal "$(printf '%s' "${status_b}" | json_field recovery.attemptsRemaining)" 3 "selected 8.0.55 fresh recovery budget"
pid_b="$(runtime_cpa_pid)"
assert_not_equal "${pid_b}" "${pid_a}" "selected stage child replacement"
assert_equal "$(cpa_exe_path "${pid_b}")" "${rt_root}/supervisor/artifacts/cpa/8.0.55/cli-proxy-api" "selected 8.0.55 finalized stage executable"
assert_cpa_identity "${pid_b}"
assert_selection 8.0.55 "${artifact_a}"
assert_secret_and_private_state_denied "${pid_b}"
assert_execution_corridor "${pid_b}" 8.0.55

same_pin_c="$(
  printf '{"operationId":"runtime19-same-pin","expectedRuntimeIdentity":"embedded-default","expectedRuntimeGeneration":%s,"expectedActiveArtifactId":"%s","targetVersion":"8.0.55"}' \
    "${generation_stage}" "${artifact_a}" |
    post_runtime_json "${cid_c}" "/v1/runtime/operations/activate-update"
)"
assert_equal "$(printf '%s' "${same_pin_c}" | json_field status)" 409 "same-pin activate-update HTTP status"
assert_equal "$(printf '%s' "${same_pin_c}" | json_field body.error.code)" operation_state_conflict "same-pin activate-update error code"
assert_equal "$(runtime_cpa_pid)" "${pid_b}" "same-pin 409 preserved selected child"

private_gateway_b="$(private_gateway_signature)"
wait_public_gateway_signature "${private_gateway_b}" 60 >/dev/null
journal_b="$(snapshot_runtime_journal scenario-c-stage-journal)"
assert_equal "$(journal_count "${journal_b}" start "${generation_a}" 'runtime-reconcile/v2:' succeeded -)" 1 "initial generation Start count"
assert_equal "$(journal_count "${journal_b}" start "${generation_stage}" 'runtime-reconcile/v2:' succeeded -)" 1 "selected stage generation Start count"
assert_equal "$(journal_count "${journal_b}" activate_update - - - -)" 0 "409 pre-admission guards leave no journal entries"

scenario 'D — Runtime recreate / selected 8.0.55 persistence'
paused_manager_pid="$(service_pid cpa-manager-plus)"
"${compose[@]}" exec -T cpa-manager-plus sh -ec 'kill -STOP "$1"' sh "${paused_manager_pid}"
"${compose[@]}" exec -T cpa-manager-plus sh -ec 'kill -TERM $(pidof cpamp-runtime-supervisor)'
sleep 1
status_b_inactive="$(wait_runtime_observation offline inactive "${artifact_a}" local 60)"
generation_b="$(printf '%s' "${status_b_inactive}" | json_uint_field runtimeGeneration)"
assert_not_equal "${generation_b}" "${generation_stage}" "Runtime recreation generation"
assert_selection 8.0.55 "${artifact_a}"
assert_equal "$(gateway_config_digest)" "${config_digest_a}" "Runtime recreate Gateway config digest"
assert_equal "$(runtime_secret_digest)" "${secret_digest_a}" "Runtime recreate secret digest"
journal_before_b_reconcile="$(snapshot_runtime_journal scenario-d-before-reconcile)"
assert_equal "$(journal_count "${journal_before_b_reconcile}" start "${generation_b}" 'runtime-reconcile/v2:' - -)" 0 "new generation pre-reconcile Start count"
"${compose[@]}" exec -T cpa-manager-plus sh -ec 'kill -CONT "$1"' sh "${paused_manager_pid}"
paused_manager_pid=""
status_b_recreated="$(wait_runtime_observation ready armed "${artifact_a}" local 120)"
assert_equal "$(printf '%s' "${status_b_recreated}" | json_uint_field runtimeGeneration)" "${generation_b}" "recreated selected 8.0.55 generation"
pid_b_recreated="$(runtime_cpa_pid)"
assert_equal "$(cpa_exe_path "${pid_b_recreated}")" "${rt_root}/supervisor/artifacts/cpa/8.0.55/cli-proxy-api" "recreated selected 8.0.55 executable"
assert_cpa_identity "${pid_b_recreated}"
assert_selection 8.0.55 "${artifact_a}"
wait_public_health 90
private_gateway_b_recreated="$(private_gateway_signature)"
wait_public_gateway_signature "${private_gateway_b_recreated}" 60 >/dev/null
journal_b_recreated="$(snapshot_runtime_journal scenario-d-recreated-journal)"
assert_equal "$(journal_count "${journal_b_recreated}" start "${generation_b}" 'runtime-reconcile/v2:' succeeded -)" 1 "selected 8.0.55 generation Start evidence"
manager_b_recreated="$(snapshot_manager_database scenario-d-manager)"
read -r desired_b revision_b updated_at_b < <(desired_observation "${manager_b_recreated}")
assert_equal "${desired_b}" running "Runtime recreate desired lifecycle"
assert_equal "${revision_b}" "${revision_a}" "Runtime recreate desired revision"
assert_equal "${updated_at_b}" "${updated_at_a}" "Runtime recreate desired timestamp"

scenario 'E — automatic recovery of selected 8.0.55'
pid_b_before_recovery="$(runtime_cpa_pid)"
"${compose[@]}" exec -T cpa-manager-plus sh -ec 'kill -KILL "$1"' sh "${pid_b_before_recovery}"
status_b_recovered="$(wait_runtime_observation ready armed "${artifact_a}" local 120)"
assert_equal "$(printf '%s' "${status_b_recovered}" | json_uint_field runtimeGeneration)" "${generation_b}" "selected 8.0.55 recovery RuntimeGeneration"
assert_equal "$(printf '%s' "${status_b_recovered}" | json_field recovery.attemptsRemaining)" 2 "selected 8.0.55 recovery remaining budget"
pid_b_recovered="$(runtime_cpa_pid)"
assert_not_equal "${pid_b_recovered}" "${pid_b_before_recovery}" "selected 8.0.55 recovery child replacement"
assert_equal "$(cpa_exe_path "${pid_b_recovered}")" "${rt_root}/supervisor/artifacts/cpa/8.0.55/cli-proxy-api" "recovered selected 8.0.55 executable"
assert_cpa_identity "${pid_b_recovered}"
assert_selection 8.0.55 "${artifact_a}"
journal_b_recovery="$(snapshot_runtime_journal scenario-e-recovery-journal)"
assert_equal "$(journal_count "${journal_b_recovery}" auto_recovery "${generation_b}" 'auto-recovery-' succeeded -)" 1 "selected 8.0.55 successful recovery evidence"
private_gateway_b_recovered="$(private_gateway_signature)"
wait_public_gateway_signature "${private_gateway_b_recovered}" 60 >/dev/null

scenario 'F — failed candidate C / rollback exact 8.0.55'
artifact_c="$(seed_negative_fault_stage 7.3.5 /bin/true)"
activate_c="$(activate runtime19-activate-c "${generation_b}" "${artifact_a}" 7.3.5)"
assert_equal "$(printf '%s' "${activate_c}" | json_field state)" failed "candidate C activation state"
failure_code_c="$(printf '%s' "${activate_c}" | json_field error.code)"
case "${failure_code_c}" in
  activation_start_failed|activation_readiness_failed) ;;
  *) fail "candidate C failure code is ${failure_code_c}" ;;
esac
"${compose[@]}" exec -T cpa-manager-plus sh -ec 'chmod 700 "$1" && rm -rf "$1"' sh "${rt_root}/supervisor/artifacts/cpa/7.3.5"
status_after_c="$(wait_runtime_observation ready armed "${artifact_a}" local 120)"
assert_equal "$(printf '%s' "${status_after_c}" | json_uint_field runtimeGeneration)" "${generation_b}" "C rollback RuntimeGeneration"
assert_equal "$(printf '%s' "${status_after_c}" | json_field recovery.attemptsRemaining)" 3 "rollback 8.0.55 recovery budget"
pid_after_c="$(runtime_cpa_pid)"
assert_equal "$(cpa_exe_path "${pid_after_c}")" "${rt_root}/supervisor/artifacts/cpa/8.0.55/cli-proxy-api" "rollback selected 8.0.55 executable"
assert_cpa_identity "${pid_after_c}"
assert_selection 8.0.55 "${artifact_a}"
private_gateway_after_c="$(private_gateway_signature)"
wait_public_gateway_signature "${private_gateway_after_c}" 60 >/dev/null
journal_after_c="$(snapshot_runtime_journal scenario-f-rollback-journal)"
assert_equal "$(journal_count "${journal_after_c}" activate_update - - - -)" 1 "activation operation count"
assert_equal "$(journal_count "${journal_after_c}" activate_update "${generation_b}" runtime19-activate-c failed "${failure_code_c}")" 1 "candidate C failed evidence"

scenario 'G — selected 8.0.55 recovery exhaustion / manual-intervention fence'
selected_b="${rt_root}/supervisor/artifacts/cpa/8.0.55/cli-proxy-api"
selected_b_restore="${rt_root}/supervisor/artifacts/cpa/8.0.55/cli-proxy-api.runtime19-restore"
"${compose[@]}" exec -T cpa-manager-plus sh -ec '
  test -x "$1"
  test ! -e "$2"
  test -x /usr/local/bin/cli-proxy-api
  chmod 710 "$(dirname "$1")"
  mv "$1" "$2"
  chmod 510 "$(dirname "$1")"
' sh "${selected_b}" "${selected_b_restore}"
stage_restore_moved="true"
assert_cpa_pid_alive "${pid_after_c}"
wait_public_gateway_signature "${private_gateway_after_c}" 30 >/dev/null
"${compose[@]}" exec -T cpa-manager-plus sh -ec 'kill -KILL "$1"' sh "${pid_after_c}"
status_manual="$(wait_runtime_observation offline manual_intervention - local 120)"
assert_equal "$(printf '%s' "${status_manual}" | json_uint_field runtimeGeneration)" "${generation_b}" "manual-intervention generation"
assert_equal "$(printf '%s' "${status_manual}" | json_field recovery.attemptsRemaining)" 0 "manual-intervention recovery budget"
assert_no_cpa
assert_selection 8.0.55 "${artifact_a}"
journal_manual="$(snapshot_runtime_journal scenario-g-manual-journal)"
assert_equal "$(journal_count "${journal_manual}" auto_recovery "${generation_b}" 'auto-recovery-' failed process_recovery_start_failed)" 3 "selected 8.0.55 bounded failed recovery evidence"
assert_equal "$(journal_count "${journal_manual}" auto_recovery "${generation_b}" 'auto-recovery-' succeeded -)" 1 "selected 8.0.55 prior successful recovery evidence"
manual_operation_count="$(journal_count "${journal_manual}" - - - - -)"

# Cross at least two Manager reconcile intervals. Desired running must not
# bypass the generation-scoped manual-intervention fence.
sleep 12
journal_manual_fenced="$(snapshot_runtime_journal scenario-g-fenced-journal)"
assert_equal "$(journal_count "${journal_manual_fenced}" - - - - -)" "${manual_operation_count}" "same-generation manual fence journal size"
assert_equal "$(journal_count "${journal_manual_fenced}" start "${generation_b}" 'runtime-reconcile/v2:' succeeded -)" 1 "same-generation manual fence Start count"
manager_manual="$(snapshot_manager_database scenario-g-manager)"
read -r desired_manual revision_manual updated_at_manual < <(desired_observation "${manager_manual}")
assert_equal "${desired_manual}" running "manual-intervention desired lifecycle"
assert_equal "${revision_manual}" "${revision_a}" "manual-intervention desired revision"
assert_equal "${updated_at_manual}" "${updated_at_a}" "manual-intervention desired timestamp"

"${compose[@]}" exec -T cpa-manager-plus sh -ec '
  test ! -e "$1"
  test -e "$2"
  chmod 710 "$(dirname "$1")"
  mv "$2" "$1"
  chmod 510 "$(dirname "$1")"
  test -x "$1"
' sh "${selected_b}" "${selected_b_restore}"
stage_restore_moved="false"
sleep 12
status_manual_repaired="$(wait_runtime_observation offline manual_intervention - local 10)"
assert_equal "$(printf '%s' "${status_manual_repaired}" | json_uint_field runtimeGeneration)" "${generation_b}" "repaired same-generation manual fence"
assert_no_cpa
journal_manual_repaired="$(snapshot_runtime_journal scenario-g-repaired-journal)"
assert_equal "$(journal_count "${journal_manual_repaired}" - - - - -)" "${manual_operation_count}" "repair-only journal size"

paused_manager_pid="$(service_pid cpa-manager-plus)"
"${compose[@]}" exec -T cpa-manager-plus sh -ec 'kill -STOP "$1"' sh "${paused_manager_pid}"
"${compose[@]}" exec -T cpa-manager-plus sh -ec 'kill -TERM $(pidof cpamp-runtime-supervisor)'
sleep 1
status_new_generation_inactive="$(wait_runtime_observation offline inactive "${artifact_a}" local 60)"
generation_repaired="$(printf '%s' "${status_new_generation_inactive}" | json_uint_field runtimeGeneration)"
assert_not_equal "${generation_repaired}" "${generation_b}" "manual repair new RuntimeGeneration"
assert_selection 8.0.55 "${artifact_a}"
journal_before_repaired_start="$(snapshot_runtime_journal scenario-g-before-repaired-start)"
assert_equal "$(journal_count "${journal_before_repaired_start}" start "${generation_repaired}" 'runtime-reconcile/v2:' - -)" 0 "repaired generation pre-reconcile Start count"
"${compose[@]}" exec -T cpa-manager-plus sh -ec 'kill -CONT "$1"' sh "${paused_manager_pid}"
paused_manager_pid=""
status_repaired="$(wait_runtime_observation ready armed "${artifact_a}" local 120)"
assert_equal "$(printf '%s' "${status_repaired}" | json_uint_field runtimeGeneration)" "${generation_repaired}" "repaired 8.0.55 ready generation"
pid_repaired="$(runtime_cpa_pid)"
assert_equal "$(cpa_exe_path "${pid_repaired}")" "${rt_root}/supervisor/artifacts/cpa/8.0.55/cli-proxy-api" "repaired selected 8.0.55 executable"
assert_cpa_identity "${pid_repaired}"
assert_selection 8.0.55 "${artifact_a}"
assert_equal "$(gateway_config_digest)" "${config_digest_a}" "manual repair Gateway config digest"
assert_equal "$(runtime_secret_digest)" "${secret_digest_a}" "manual repair secret digest"
private_gateway_repaired="$(private_gateway_signature)"
wait_public_health 90
wait_public_gateway_signature "${private_gateway_repaired}" 60 >/dev/null
journal_repaired="$(snapshot_runtime_journal scenario-g-repaired-generation-journal)"
assert_equal "$(journal_count "${journal_repaired}" start "${generation_repaired}" 'runtime-reconcile/v2:' succeeded -)" 1 "repaired generation exact 8.0.55 Start evidence"
assert_equal "$(journal_count "${journal_repaired}" auto_recovery "${generation_b}" 'auto-recovery-' failed process_recovery_start_failed)" 3 "manual recovery history after new generation"

scenario 'H — full persisted stack restart / final isolation audit'
"${compose[@]}" down
"${compose[@]}" up "${up_args[@]}"
resolve_public_origin
wait_public_health 120
status_final="$(wait_runtime_observation ready armed "${artifact_a}" manager 120)"
generation_final="$(printf '%s' "${status_final}" | json_uint_field runtimeGeneration)"
assert_not_equal "${generation_final}" "${generation_repaired}" "full restart RuntimeGeneration"
pid_final="$(runtime_cpa_pid)"
assert_equal "$(cpa_exe_path "${pid_final}")" "${rt_root}/supervisor/artifacts/cpa/8.0.55/cli-proxy-api" "final selected 8.0.55 executable"
assert_cpa_identity "${pid_final}"
assert_selection 8.0.55 "${artifact_a}"
assert_equal "$(gateway_config_digest)" "${config_digest_a}" "full restart Gateway config digest"
assert_equal "$(runtime_secret_digest)" "${secret_digest_a}" "full restart secret digest"

manager_final="$(snapshot_manager_database scenario-h-manager)"
read -r desired_final revision_final updated_at_final < <(desired_observation "${manager_final}")
assert_equal "${desired_final}" running "full restart desired lifecycle"
assert_equal "${revision_final}" "${revision_a}" "full restart desired revision"
assert_equal "${updated_at_final}" "${updated_at_a}" "full restart desired timestamp"

journal_final="$(snapshot_runtime_journal scenario-h-final-journal)"
assert_equal "$(journal_count "${journal_final}" start "${generation_a}" 'runtime-reconcile/v2:' succeeded -)" 1 "initial generation Start continuity"
assert_equal "$(journal_count "${journal_final}" start "${generation_stage}" 'runtime-reconcile/v2:' succeeded -)" 1 "stage upload generation Start continuity"
assert_equal "$(journal_count "${journal_final}" start "${generation_b}" 'runtime-reconcile/v2:' succeeded -)" 1 "selected 8.0.55 generation Start continuity"
assert_equal "$(journal_count "${journal_final}" start "${generation_repaired}" 'runtime-reconcile/v2:' succeeded -)" 1 "repaired generation Start continuity"
assert_equal "$(journal_count "${journal_final}" start "${generation_final}" 'runtime-reconcile/v2:' succeeded -)" 1 "full restart generation Start continuity"
assert_equal "$(journal_count "${journal_final}" start - 'runtime-reconcile/v2:' succeeded -)" 5 "total generation-scoped Start continuity"
assert_equal "$(journal_count "${journal_final}" activate_update - - - -)" 1 "activation rollback history continuity"
assert_equal "$(journal_count "${journal_final}" auto_recovery - - - -)" 4 "auto-recovery history continuity"
assert_equal "$(journal_count "${journal_final}" stop - - - -)" 0 "no explicit Stop side effects"
assert_equal "$(journal_count "${journal_final}" restart - - - -)" 0 "no explicit Restart side effects"
assert_equal "$(journal_count "${journal_final}" auto_recovery "${generation_b}" 'auto-recovery-' succeeded -)" 1 "successful selected 8.0.55 recovery continuity"
assert_equal "$(journal_count "${journal_final}" auto_recovery "${generation_b}" 'auto-recovery-' failed process_recovery_start_failed)" 3 "manual-intervention failure continuity"
assert_manager_starts_are_reconcile_operations "${journal_final}"

assert_gateway_writable "${pid_final}"
assert_secret_and_private_state_denied "${pid_final}"
assert_execution_corridor "${pid_final}" 8.0.55
assert_mount_isolation
private_gateway_final="$(private_gateway_signature)"
public_gateway_final="$(wait_public_gateway_signature "${private_gateway_final}" 60)"
assert_equal "${public_gateway_final}" "${private_gateway_final}" "final public/private Gateway semantics"

echo "Runtime19 Phase1 exit E2E passed: pinned=8.0.55(${artifact_a}) C=${artifact_c}"
echo "Runtime19 generations: fresh=${generation_a} uploaded=${generation_stage} selected=${generation_b} repaired=${generation_repaired} final=${generation_final}"
echo "Runtime19 continuity: desiredRevision=${revision_a} gatewayConfig=${config_digest_a} runtimeSecret=${secret_digest_a} CFailure=${failure_code_c}"
