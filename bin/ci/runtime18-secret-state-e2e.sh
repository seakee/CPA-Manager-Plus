#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# The Dockerfiles fail closed on identity; bind a local build to this checkout.
export VERSION="${VERSION:-dev}"
export SOURCE_COMMIT="${SOURCE_COMMIT:-$(git -C "${repo_root}" rev-parse HEAD)}"
project="${CPAMP_RUNTIME18_PROJECT:-${CPAMP_SMOKE_PROJECT:-cpamp-runtime18-${RANDOM}-$$}}"
public_port="${CPAMP_RUNTIME18_PORT:-${CPAMP_SMOKE_PORT:-${CPAMP_PUBLIC_PORT:-31317}}}"
skip_build="${CPAMP_RUNTIME18_SKIP_BUILD:-${CPAMP_SMOKE_SKIP_BUILD:-false}}"
initializer_name="${project}-cpamp-manager-init"
private_dir="$(mktemp -d "${TMPDIR:-/tmp}/cpamp-runtime18-priv.XXXXXX")"
chmod 700 "${private_dir}"
up_args=(-d)
if [ "${skip_build}" = "true" ]; then
  up_args+=(--no-build)
fi
compose_override="${CPAMP_SMOKE_COMPOSE_OVERRIDE:-}"
compose=(docker compose --project-directory "${repo_root}" -f "${repo_root}/docker-compose.yml")
if [ -n "${compose_override}" ]; then
  if [ ! -f "${compose_override}" ]; then
    echo "CPAMP_SMOKE_COMPOSE_OVERRIDE does not exist or is not a regular file: ${compose_override}" >&2
    rm -rf "${private_dir}"
    exit 2
  fi
  compose_override="$(cd "$(dirname "${compose_override}")" && pwd)/$(basename "${compose_override}")"
  export CPAMP_SMOKE_COMPOSE_OVERRIDE="${compose_override}"
  compose+=(-f "${compose_override}")
fi
compose+=(-p "${project}")

paused_manager_pid=""
bundled_hold_path="/usr/local/bin/cli-proxy-api.runtime18-bundled-hold"

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
  "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
  rm -rf "${private_dir}"
}
trap cleanup EXIT HUP INT TERM

fail() {
  echo "Runtime18 secret/state E2E: $*" >&2
  exit 1
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

runtime_get() {
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

wait_ready() {
  local expected_artifact="$1"
  local attempts="${2:-120}"
  local count=0
  while [ "${count}" -lt "${attempts}" ]; do
    local status state artifact_id
    if status="$(runtime_get /v1/runtime/status 2>/dev/null)"; then
      state="$(printf '%s' "${status}" | json_field state 2>/dev/null || true)"
      artifact_id="$(printf '%s' "${status}" | json_field activeGatewayArtifact.artifactId 2>/dev/null || true)"
      if [ "${state}" = "ready" ] && [ "${artifact_id}" = "${expected_artifact}" ]; then
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
        printf "%s" "${process_dir##*/}"
        exit 0
      fi
    done
    exit 1
  '
}

assert_cpa_identity() {
  local pid="$1"
  local actual_uid actual_gid actual_groups expected_uid expected_gid
  actual_uid="$("${compose[@]}" exec -T cpa-manager-plus awk '$1 == "Uid:" { print $2; exit }' "/proc/${pid}/status")"
  actual_gid="$("${compose[@]}" exec -T cpa-manager-plus awk '$1 == "Gid:" { print $2; exit }' "/proc/${pid}/status")"
  expected_uid="$("${compose[@]}" exec -T cpa-manager-plus sh -ec 'printf %s "${CPAMP_RUNTIME_CPA_UID:-10001}"')"
  expected_gid="$("${compose[@]}" exec -T cpa-manager-plus sh -ec 'printf %s "${CPAMP_RUNTIME_CPA_GID:-10001}"')"
  actual_groups="$("${compose[@]}" exec -T cpa-manager-plus awk '$1 == "Groups:" { $1 = ""; sub(/^ +/, ""); print; exit }' "/proc/${pid}/status")"
  [ "${actual_uid}" = "${expected_uid}" ] || fail "CPA PID ${pid} UID=${actual_uid}, want ${expected_uid}"
  [ "${actual_gid}" = "${expected_gid}" ] || fail "CPA PID ${pid} GID=${expected_gid}, want ${expected_gid}"
  [ "${actual_groups}" = "${expected_gid}" ] || fail "CPA PID ${pid} supplementary groups=${actual_groups}, want only ${expected_gid}"
  [ "${actual_uid}" != "0" ] && [ "${actual_gid}" != "0" ] || fail "CPA PID ${pid} retained root identity"
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
    marker="${rt_root}/gateway/.runtime18-cpa-write"
    printf runtime18 >"${marker}"
    test "$(cat "${marker}")" = runtime18
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
    fail "CPA identity can enumerate Supervisor state"
  fi
  if "${compose[@]}" exec -T --user "${uid}:${gid}" cpa-manager-plus cat "${rt_root}/supervisor/operations.sqlite" >/dev/null 2>&1; then
    fail "CPA identity can read the Supervisor journal"
  fi
  if "${compose[@]}" exec -T --user "${uid}:${gid}" cpa-manager-plus cat "${rt_root}/supervisor/active/cpa/selection.json" >/dev/null 2>&1; then
    fail "CPA identity can read active selection"
  fi
  if "${compose[@]}" exec -T --user "${uid}:${gid}" cpa-manager-plus cat "${rt_root}/supervisor/artifacts/cpa/.runtime18-private/marker" >/dev/null 2>&1; then
    fail "CPA identity can enter a Supervisor-private staging workspace"
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
    test ! -w "$1/supervisor"
    test -r "$2"
    ! touch "$3/.runtime18-write-probe"
  ' sh "${rt_root}" "${token_file}" "${token_dir}" >/dev/null 2>&1 || fail "Manager runtime/token isolation contract changed"
  "${compose[@]}" exec -T --user 10003:10003 cpa-manager-plus sh -ec '
    ! ls /data >/dev/null 2>&1
    ! test -r "$2"
    ! ls "$1/supervisor" >/dev/null 2>&1
  ' sh "${rt_root}" "${token_file}" || fail "Ingress gained Manager, Runtime, or token storage access"
}

assert_execution_corridor() {
  local pid="$1"
  local version="$2"
  local uid gid rt_root executable
  uid="$("${compose[@]}" exec -T cpa-manager-plus awk '$1 == "Uid:" { print $2; exit }' "/proc/${pid}/status")"
  gid="$("${compose[@]}" exec -T cpa-manager-plus awk '$1 == "Gid:" { print $2; exit }' "/proc/${pid}/status")"
  rt_root="$(runtime_root)"
  executable="${rt_root}/supervisor/artifacts/cpa/${version}/cli-proxy-api"
  "${compose[@]}" exec -T --user "${uid}:${gid}" cpa-manager-plus test -r "${executable}" || fail "CPA cannot read finalized ${version} executable"
  "${compose[@]}" exec -T --user "${uid}:${gid}" cpa-manager-plus test -x "${executable}" || fail "CPA cannot execute finalized ${version} executable"
  if "${compose[@]}" exec -T --user "${uid}:${gid}" cpa-manager-plus ls "${rt_root}/supervisor/artifacts/cpa" >/dev/null 2>&1; then
    fail "artifact execution corridor permits stage enumeration"
  fi
}

container_id() {
  local id
  id="$("${compose[@]}" ps --all -q cpa-manager-plus)"
  test -n "${id}"
  printf '%s' "${id}"
}

post_runtime_json() {
  local cid="$1"
  local endpoint="$2"
  local body_file="${private_dir}/post-body.json"
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

assert_runtime_token_preserved() {
  local label="$1"
  local current_file current_digest current_mode
  current_file="$(runtime_token_file)"
  [ "${current_file}" = "${token_file_a}" ] ||
    fail "${label}: runtime token file path changed (${current_file} != ${token_file_a})"
  current_digest="$("${compose[@]}" exec -T cpa-manager-plus sha256sum "${current_file}" | awk '{print $1}')"
  [ "${current_digest}" = "${token_digest_a}" ] ||
    fail "${label}: runtime transport token digest rotated across recreate"
  current_mode="$("${compose[@]}" exec -T cpa-manager-plus stat -c '%u:%g:%a' "${current_file}")"
  [ "${current_mode}" = "0:10002:640" ] ||
    fail "${label}: runtime token file owner/mode changed to ${current_mode}"
}

assert_client_models_ok() {
  local label="$1"
  local resp_file="${private_dir}/models-response.json"
  local status
  status="$( (
    umask 077
    curl -sS --max-time 5 --config "${client_auth_curl_cfg}" \
      -o "${resp_file}" -w '%{http_code}' "${public_origin}/v1/models"
  ) )"
  [ "${status}" = "200" ] || {
    rm -f "${resp_file}"
    fail "${label}: /v1/models returned HTTP ${status}, want 200"
  }
  node -e '
    const body = JSON.parse(require("node:fs").readFileSync(process.argv[1], "utf8"));
    if (!body || typeof body !== "object" || body.object !== "list" || !Array.isArray(body.data)) {
      process.exit(2);
    }
  ' "${resp_file}" || {
    rm -f "${resp_file}"
    fail "${label}: /v1/models did not return OpenAI model list JSON with data array"
  }
  rm -f "${resp_file}"
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
    if status="$(runtime_get /v1/runtime/status 2>/dev/null)"; then
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

wait_state() {
  local wanted="$1"
  local attempts="${2:-60}"
  local count=0
  while [ "${count}" -lt "${attempts}" ]; do
    local status
    if status="$(runtime_get /v1/runtime/status 2>/dev/null)" &&
      [ "$(printf '%s' "${status}" | json_field state 2>/dev/null || true)" = "${wanted}" ]; then
      printf '%s' "${status}"
      return 0
    fi
    count=$((count + 1))
    sleep 1
  done
  return 1
}

# Negative rollback fault-injection only: stages a non-CPA system binary under a
# non-v8 legacy version directory strictly to test ActivateUpdate spawn/readiness
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

wait_recovery_replacement() {
  local old_pid="$1"
  local expected_artifact="$2"
  local count=0
  while [ "${count}" -lt 120 ]; do
    local pid status state artifact
    pid="$(runtime_cpa_pid 2>/dev/null || true)"
    if [ -n "${pid}" ] && [ "${pid}" != "${old_pid}" ] && status="$(runtime_get /v1/runtime/status 2>/dev/null)"; then
      state="$(printf '%s' "${status}" | json_field state 2>/dev/null || true)"
      artifact="$(printf '%s' "${status}" | json_field activeGatewayArtifact.artifactId 2>/dev/null || true)"
      if [ "${state}" = ready ] && [ "${artifact}" = "${expected_artifact}" ]; then
        printf '%s' "${pid}"
        return 0
      fi
    fi
    count=$((count + 1))
    sleep 1
  done
  return 1
}

assert_runtime_bootstrap_fails_closed() {
  local expected_log="$1"
  local count=0
  "${compose[@]}" exec -T cpa-manager-plus sh -ec '
    kill -TERM $(pidof cpamp-runtime-supervisor) 2>/dev/null || true
    kill -KILL $(pidof cli-proxy-api) 2>/dev/null || true
  ' >/dev/null 2>&1 || "${compose[@]}" up "${up_args[@]}" --force-recreate cpa-manager-plus >/dev/null 2>&1 || true
  while [ "${count}" -lt 30 ]; do
    if "${compose[@]}" logs --no-color --tail 100 cpa-manager-plus 2>&1 | grep -Fq "${expected_log}"; then
      if runtime_cpa_pid >/dev/null 2>&1; then
        fail "untrusted persisted state started CPA"
      fi
      if runtime_get /v1/runtime/status >/dev/null 2>&1; then
        fail "untrusted persisted state left Runtime Supervisor reachable"
      fi
      return 0
    fi
    count=$((count + 1))
    sleep 1
  done
  "${compose[@]}" logs --no-color --tail 100 cpa-manager-plus >&2 || true
  fail "untrusted persisted state did not produce expected failure: ${expected_log}"
}

repair_persisted_owner_and_restart() {
  local persisted_path="$1"
  "${compose[@]}" stop cpa-manager-plus >/dev/null 2>&1 || true
  "${compose[@]}" run --rm --no-deps --entrypoint sh cpa-manager-plus -ec '
    chown root:root "$1"
  ' sh "${persisted_path}" >/dev/null
  "${compose[@]}" up "${up_args[@]}" --force-recreate cpa-manager-plus >/dev/null
  wait_ready "${artifact_a}" >/dev/null || fail "Runtime did not recover after repairing ${persisted_path} owner"
  resolve_public_origin
  assert_runtime_token_preserved "after repairing ${persisted_path} owner"
}

cd "${repo_root}"
export CPAMP_PUBLIC_PORT="${public_port}"
if [ "${skip_build}" != "true" ]; then
  "${compose[@]}" build
fi
# Seed the disposable Manager reconciler baseline while /data/runtime starts
# empty on the default CPA v8.0.55 full bundle.
"${repo_root}/bin/ci/runtime-lifecycle-manager-baseline.sh" "${project}" "${initializer_name}"
"${compose[@]}" up "${up_args[@]}"
resolve_public_origin
retry 120 curl --fail --silent "${public_origin}/health" >/dev/null
retry 120 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null

rt_root="$(runtime_root)"
"${compose[@]}" exec -T cpa-manager-plus sh -ec '
  test "$(awk '\''$1 == "Uid:" { print $2; exit }'\'' /proc/1/status)" = 0
  test -f "$1/.cpamp-v8-bootstrap"
  test "$(stat -c %u:%g:%a "$1/.cpamp-v8-bootstrap")" = "0:0:444"
  grep -q "^config-version: 8$" "$1/gateway/config.yaml"
  mkdir -p "$1/supervisor/artifacts/cpa/.runtime18-private"
  chmod 700 "$1/supervisor/artifacts/cpa/.runtime18-private"
  printf private >"$1/supervisor/artifacts/cpa/.runtime18-private/marker"
  chmod 600 "$1/supervisor/artifacts/cpa/.runtime18-private/marker"
' sh "${rt_root}"
token_file_a="$(runtime_token_file)"
token_digest_a="$("${compose[@]}" exec -T cpa-manager-plus sha256sum "${token_file_a}" | awk '{print $1}')"
[ -n "${token_digest_a}" ] || fail "initial runtime transport token digest is empty"
[ "$("${compose[@]}" exec -T cpa-manager-plus stat -c '%u:%g:%a' "${token_file_a}")" = "0:10002:640" ] ||
  fail "initial runtime transport token file owner/mode is not 0:10002:640"

status_a="$(runtime_get /v1/runtime/status)"
artifact_a="$(printf '%s' "${status_a}" | json_field activeGatewayArtifact.artifactId)"
[ "$(printf '%s' "${status_a}" | json_field activeGatewayArtifact.version)" = "8.0.55" ] || fail "bundled A version is not 8.0.55"
[ "$(printf '%s' "${status_a}" | json_field cpaObservedVersion)" = "8.0.55" ] || fail "bundled A cpaObservedVersion is not 8.0.55"
generation_a="$(printf '%s' "${status_a}" | json_uint_field runtimeGeneration)"
pid_a="$(runtime_cpa_pid)"
[ "$(cpa_exe_path "${pid_a}")" = "/usr/local/bin/cli-proxy-api" ] || fail "initial CPA did not execute bundled 8.0.55 binary"
assert_cpa_identity "${pid_a}"
assert_gateway_writable "${pid_a}"
assert_secret_and_private_state_denied "${pid_a}"
assert_mount_isolation

# Transition from bundled 8.0.55 to finalized_stage 8.0.55 + selection.json via
# the real Supervisor PUT /v1/runtime/artifact/upload API (positive A -> B
# activate-update requires two distinct CPA binaries and returns 409
# operation_state_conflict on a single fixed 8.0.55 pin).
cid="$(container_id)"
paused_manager_pid="$("${compose[@]}" exec -T cpa-manager-plus sh -ec 'pidof cpa-manager-plus | awk "{print \$1}"')"
"${compose[@]}" exec -T cpa-manager-plus sh -ec '
  kill -STOP "$1"
  test -x /usr/local/bin/cli-proxy-api
  test ! -e "$2"
  mv /usr/local/bin/cli-proxy-api "$2"
  kill -TERM $(pidof cpamp-runtime-supervisor)
  kill -KILL $(pidof cli-proxy-api) 2>/dev/null || true
' sh "${paused_manager_pid}" "${bundled_hold_path}"
status_unbundled="$(wait_offline_new_generation "${generation_a}" 60)"
generation_stage="$(printf '%s' "${status_unbundled}" | json_uint_field runtimeGeneration)"
uploaded_artifact="$(python3 "${repo_root}/bin/ci/runtime-v8-upload-stage.py" upload-pinned \
  "${cid}" "${bundled_hold_path}" "${rt_root}")"
[ "${uploaded_artifact}" = "${artifact_a}" ] || fail "uploaded 8.0.55 artifact digest mismatch"
"${compose[@]}" exec -T cpa-manager-plus sh -ec '
  mv "$1" /usr/local/bin/cli-proxy-api
  test -x /usr/local/bin/cli-proxy-api
  kill -CONT "$2"
' sh "${bundled_hold_path}" "${paused_manager_pid}"
paused_manager_pid=""

status_b="$(wait_ready "${artifact_a}")"
[ "$(printf '%s' "${status_b}" | json_field recovery.state)" = armed ] || fail "selected 8.0.55 recovery is not armed"
pid_b="$(runtime_cpa_pid)"
[ "${pid_b}" != "${pid_a}" ] || fail "stage selection did not replace A"
[ "$(cpa_exe_path "${pid_b}")" = "${rt_root}/supervisor/artifacts/cpa/8.0.55/cli-proxy-api" ] || fail "CPA did not execute finalized 8.0.55 stage"
assert_cpa_identity "${pid_b}"
assert_secret_and_private_state_denied "${pid_b}"
assert_execution_corridor "${pid_b}" 8.0.55
retry 30 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null

same_pin_resp="$(
  printf '{"operationId":"runtime18-same-pin","expectedRuntimeIdentity":"embedded-default","expectedRuntimeGeneration":%s,"expectedActiveArtifactId":"%s","targetVersion":"8.0.55"}' \
    "${generation_stage}" "${artifact_a}" |
    post_runtime_json "${cid}" "/v1/runtime/operations/activate-update"
)"
[ "$(printf '%s' "${same_pin_resp}" | json_field status)" = "409" ] || fail "same-pin activate-update did not return 409"
[ "$(printf '%s' "${same_pin_resp}" | json_field body.error.code)" = "operation_state_conflict" ] || fail "same-pin activate-update error code mismatch"

"${compose[@]}" exec -T cpa-manager-plus sh -ec 'kill -TERM $(pidof cpamp-runtime-supervisor)'
status_recreated="$(wait_ready "${artifact_a}")"
generation_b="$(printf '%s' "${status_recreated}" | json_uint_field runtimeGeneration)"
[ "${generation_b}" != "${generation_stage}" ] || fail "Runtime recreation did not rotate generation"
pid_recreated="$(runtime_cpa_pid)"
[ "$(cpa_exe_path "${pid_recreated}")" = "${rt_root}/supervisor/artifacts/cpa/8.0.55/cli-proxy-api" ] || fail "recreated CPA did not execute finalized 8.0.55 stage"
assert_cpa_identity "${pid_recreated}"
assert_gateway_writable "${pid_recreated}"
assert_secret_and_private_state_denied "${pid_recreated}"
assert_execution_corridor "${pid_recreated}" 8.0.55
retry 30 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null

"${compose[@]}" exec -T cpa-manager-plus sh -ec 'kill -9 "$1"' sh "${pid_recreated}"
pid_recovered="$(wait_recovery_replacement "${pid_recreated}" "${artifact_a}")"
[ "$(cpa_exe_path "${pid_recovered}")" = "${rt_root}/supervisor/artifacts/cpa/8.0.55/cli-proxy-api" ] || fail "recovered CPA did not execute finalized 8.0.55 stage"
assert_cpa_identity "${pid_recovered}"
assert_execution_corridor "${pid_recovered}" 8.0.55
retry 30 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null

artifact_d="$(seed_negative_fault_stage 7.3.6 /bin/false)"
"${compose[@]}" exec -T cpa-manager-plus chmod 500 "${rt_root}/supervisor/artifacts/cpa/7.3.6"
activate_d="$(activate runtime18-activate-d "${generation_b}" "${artifact_a}" 7.3.6)"
[ "$(printf '%s' "${activate_d}" | json_field state)" = failed ] || fail "inaccessible D did not fail closed (state=$(printf '%s' "${activate_d}" | json_field state))"
[ "$(printf '%s' "${activate_d}" | json_field error.code)" = activation_start_failed ] || fail "inaccessible D was not a spawn failure (code=$(printf '%s' "${activate_d}" | json_field error.code))"
"${compose[@]}" exec -T cpa-manager-plus sh -ec 'chmod 700 "$1" && rm -rf "$1"' sh "${rt_root}/supervisor/artifacts/cpa/7.3.6"
status_after_d="$(wait_ready "${artifact_a}")"
pid_after_d="$(runtime_cpa_pid)"
[ "$(cpa_exe_path "${pid_after_d}")" = "${rt_root}/supervisor/artifacts/cpa/8.0.55/cli-proxy-api" ] || fail "rollback after D did not respawn finalized 8.0.55 stage"
assert_cpa_identity "${pid_after_d}"
[ "$(printf '%s' "${status_after_d}" | json_field recovery.state)" = armed ] || fail "rollback after D did not arm 8.0.55 recovery"

artifact_c="$(seed_negative_fault_stage 7.3.5 /bin/true)"
activate_c="$(activate runtime18-activate-c "${generation_b}" "${artifact_a}" 7.3.5)"
[ "$(printf '%s' "${activate_c}" | json_field state)" = failed ] || fail "unready C did not fail (state=$(printf '%s' "${activate_c}" | json_field state))"
failure_code="$(printf '%s' "${activate_c}" | json_field error.code)"
case "${failure_code}" in
  activation_start_failed|activation_readiness_failed) ;;
  *) fail "unready C failure code is ${failure_code}" ;;
esac
"${compose[@]}" exec -T cpa-manager-plus sh -ec 'chmod 700 "$1" && rm -rf "$1"' sh "${rt_root}/supervisor/artifacts/cpa/7.3.5"
status_after_c="$(wait_ready "${artifact_a}")"
[ "$(printf '%s' "${status_after_c}" | json_field recovery.state)" = armed ] || fail "rollback 8.0.55 recovery is not armed"
pid_after_c="$(runtime_cpa_pid)"
[ "$(cpa_exe_path "${pid_after_c}")" = "${rt_root}/supervisor/artifacts/cpa/8.0.55/cli-proxy-api" ] || fail "rollback after C did not respawn finalized 8.0.55 stage"
assert_cpa_identity "${pid_after_c}"
assert_secret_and_private_state_denied "${pid_after_c}"
assert_execution_corridor "${pid_after_c}" 8.0.55
assert_mount_isolation
retry 30 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null

# Stage a real CPA v8 destination via POST /v1/runtime/operations/stage-destination
# and verify CPA 8.0.55 config-version: 8, management API credentials, client key
# credentials, and Supervisor stage.json secret isolation across recreate.
dest_client_key="sk-Runtime18ClientKey0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJ"
dest_mgmt_key="fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
dest_source_id="managed:sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
paused_manager_pid="$("${compose[@]}" exec -T cpa-manager-plus sh -ec 'pidof cpa-manager-plus | awk "{print \$1}"')"
"${compose[@]}" exec -T cpa-manager-plus sh -ec 'kill -STOP "$1"' sh "${paused_manager_pid}"
stop_resp="$(
  printf '{"operationId":"runtime18-stop-for-dest","expectedRuntimeIdentity":"embedded-default","expectedRuntimeGeneration":%s}' \
    "${generation_b}" |
    post_runtime_json "${cid}" "/v1/runtime/operations/stop"
)"
[ "$(printf '%s' "${stop_resp}" | json_field status)" = "200" ] || fail "stop for stage-destination failed"
[ "$(printf '%s' "${stop_resp}" | json_field body.state)" = "succeeded" ] || fail "stop operation did not succeed"
wait_state offline 30 >/dev/null || fail "Runtime did not reach offline before stage-destination"
stage_dest_guard_resp="$(
  printf '{"operationId":"runtime18-stage-dest","expectedRuntimeIdentity":"embedded-default","expectedRuntimeGeneration":%s,"expectedActiveArtifactId":"%s","sourceIdentity":"%s","sourceClass":"new_managed","apiKeys":["%s"],"managementKey":"%s"}' \
    "${generation_b}" "${artifact_a}" "${dest_source_id}" "${dest_client_key}" "${dest_mgmt_key}" |
    post_runtime_json "${cid}" "/v1/runtime/operations/stage-destination"
)"
[ "$(printf '%s' "${stage_dest_guard_resp}" | json_field status)" = "409" ] || fail "non-empty bootstrap gateway/auth did not return 409 on new_managed stage-destination"
[ "$(printf '%s' "${stage_dest_guard_resp}" | json_field body.error.code)" = "gateway_auth_present" ] || fail "non-empty bootstrap gateway/auth error code is not gateway_auth_present"
"${compose[@]}" exec -T --user 10001:10001 cpa-manager-plus sh -ec 'find "$1/gateway/auth" -mindepth 1 -maxdepth 1 -exec rm -rf {} +' sh "${rt_root}"
stage_dest_resp="$(
  printf '{"operationId":"runtime18-stage-dest","expectedRuntimeIdentity":"embedded-default","expectedRuntimeGeneration":%s,"expectedActiveArtifactId":"%s","sourceIdentity":"%s","sourceClass":"new_managed","apiKeys":["%s"],"managementKey":"%s"}' \
    "${generation_b}" "${artifact_a}" "${dest_source_id}" "${dest_client_key}" "${dest_mgmt_key}" |
    post_runtime_json "${cid}" "/v1/runtime/operations/stage-destination"
)"
[ "$(printf '%s' "${stage_dest_resp}" | json_field status)" = "200" ] || fail "stage-destination did not return 200 (status=$(printf '%s' "${stage_dest_resp}" | json_field status || true) code=$(printf '%s' "${stage_dest_resp}" | json_field body.error.code || true))"
[ "$(printf '%s' "${stage_dest_resp}" | json_field body.state)" = "succeeded" ] || fail "stage-destination did not succeed"
start_dest_resp="$(
  printf '{"operationId":"runtime18-start-dest","expectedRuntimeIdentity":"embedded-default","expectedRuntimeGeneration":%s,"expectedActiveArtifactId":"%s"}' \
    "${generation_b}" "${artifact_a}" |
    post_runtime_json "${cid}" "/v1/runtime/operations/start"
)"
[ "$(printf '%s' "${start_dest_resp}" | json_field status)" = "200" ] || fail "start after stage-destination did not return 200"
[ "$(printf '%s' "${start_dest_resp}" | json_field body.state)" = "succeeded" ] || fail "start after stage-destination did not succeed"
"${compose[@]}" exec -T cpa-manager-plus sh -ec 'kill -CONT "$1"' sh "${paused_manager_pid}"
paused_manager_pid=""
wait_ready "${artifact_a}" >/dev/null || fail "Runtime did not start staged CPA v8 destination"
pid_dest="$(runtime_cpa_pid)"
[ "$(cpa_exe_path "${pid_dest}")" = "${rt_root}/supervisor/artifacts/cpa/8.0.55/cli-proxy-api" ] || fail "destination CPA did not execute finalized 8.0.55 stage"
"${compose[@]}" exec -T cpa-manager-plus sh -ec '
  cmdline="$(tr "\000" " " </proc/"$1"/cmdline)"
  printf "%s" "${cmdline}" | grep -Fq -- "-config $2/supervisor/destination/config.yaml"
' sh "${pid_dest}" "${rt_root}" || fail "CPA 8.0.55 was not launched with staged destination config.yaml"
mgmt_auth_curl_cfg="${private_dir}/mgmt-auth.curl"
client_auth_curl_cfg="${private_dir}/client-auth.curl"
mgmt_bearer_file="${private_dir}/mgmt-bearer.token"
wrong_bearer_file="${private_dir}/wrong-bearer.token"
cpa_resp_file="${private_dir}/cpa-mgmt-response.json"
(
  umask 077
  printf 'header = "Authorization: Bearer %s"\n' "${dest_mgmt_key}" >"${mgmt_auth_curl_cfg}"
  printf 'header = "Authorization: Bearer %s"\n' "${dest_client_key}" >"${client_auth_curl_cfg}"
  printf '%s' "${dest_mgmt_key}" >"${mgmt_bearer_file}"
  printf '%s' "wrong-key" >"${wrong_bearer_file}"
)
[ "$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 "${public_origin}/v1/models")" = "401" ] || fail "unauthenticated /v1/models was not rejected after destination staging"
[ "$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 --config "${mgmt_auth_curl_cfg}" "${public_origin}/v1/models")" = "401" ] || fail "management key was accepted as client key on /v1/models"
assert_client_models_ok "initial staged destination"
python3 "${repo_root}/bin/ci/runtime-v8-upload-stage.py" cpa-get \
  "${cid}" "/v8/management/config" --out-file "${cpa_resp_file}"
[ "$(json_field status <"${cpa_resp_file}")" = "401" ] || fail "unauthenticated CPA /v8/management/config did not return HTTP 401"
python3 "${repo_root}/bin/ci/runtime-v8-upload-stage.py" cpa-get \
  "${cid}" "/v8/management/config" --bearer-file "${wrong_bearer_file}" --out-file "${cpa_resp_file}"
[ "$(json_field status <"${cpa_resp_file}")" = "401" ] || fail "wrong-key CPA /v8/management/config did not return HTTP 401"
python3 "${repo_root}/bin/ci/runtime-v8-upload-stage.py" cpa-get \
  "${cid}" "/v8/management/config" --bearer-file "${mgmt_bearer_file}" --out-file "${cpa_resp_file}"
node -e '
  const resp = JSON.parse(require("node:fs").readFileSync(process.argv[1], "utf8"));
  if (resp.status !== 200 || !resp.body || typeof resp.body !== "object" || resp.body["config-version"] !== 8) {
    process.exit(2);
  }
' "${cpa_resp_file}" || fail "CPA 8.0.55 /v8/management/config did not return HTTP 200 with config-version: 8"
python3 "${repo_root}/bin/ci/runtime-v8-upload-stage.py" cpa-get \
  "${cid}" "/v0/management/config" --bearer-file "${mgmt_bearer_file}" --out-file "${cpa_resp_file}"
node -e '
  const resp = JSON.parse(require("node:fs").readFileSync(process.argv[1], "utf8"));
  if (resp.status !== 200 || !resp.body || typeof resp.body !== "object") {
    process.exit(2);
  }
' "${cpa_resp_file}" || fail "CPA 8.0.55 /v0/management/config did not return HTTP 200 JSON object"
rm -f "${cpa_resp_file}"
"${compose[@]}" exec -T --user 10001:10001 cpa-manager-plus sh -ec '
  ! test -r "$1/supervisor/destination/stage.json"
  test -r "$1/supervisor/destination/config.yaml"
  test -w "$1/supervisor/destination/auth"
' sh "${rt_root}" || fail "CPA child destination secret isolation failed"
"${compose[@]}" exec -T --user 10002:10002 cpa-manager-plus sh -ec '
  ! test -r "$1/supervisor/destination/stage.json"
  ! test -r "$1/supervisor/destination/config.yaml"
' sh "${rt_root}" || fail "Manager gained read access to Supervisor destination secrets"
"${compose[@]}" exec -T --user 10003:10003 cpa-manager-plus sh -ec '
  ! test -r "$1/supervisor/destination/stage.json"
  ! test -r "$1/supervisor/destination/config.yaml"
' sh "${rt_root}" || fail "Ingress gained read access to Supervisor destination secrets"

"${compose[@]}" exec -T cpa-manager-plus chown 10001:10001 "${rt_root}/supervisor/active/cpa/selection.json"
assert_runtime_bootstrap_fails_closed "selection file: persisted state owner is not the Supervisor: owner UID 10001 differs from Supervisor UID 0"
repair_persisted_owner_and_restart "${rt_root}/supervisor/active/cpa/selection.json"
assert_client_models_ok "after selection.json repair recreate"

"${compose[@]}" exec -T cpa-manager-plus chown 10001:10001 "${rt_root}/supervisor/operations.sqlite"
assert_runtime_bootstrap_fails_closed "journal file: persisted state owner is not the Supervisor: owner UID 10001 differs from Supervisor UID 0"
repair_persisted_owner_and_restart "${rt_root}/supervisor/operations.sqlite"

"${compose[@]}" exec -T cpa-manager-plus chown 10001:10001 "${rt_root}/supervisor/active"
assert_runtime_bootstrap_fails_closed "selection parent: persisted state owner is not the Supervisor: owner UID 10001 differs from Supervisor UID 0"
repair_persisted_owner_and_restart "${rt_root}/supervisor/active"

gateway_symlink_target="${rt_root}/supervisor/.runtime18-gateway-symlink-target"
"${compose[@]}" exec -T cpa-manager-plus sh -ec '
  mkdir "$1"
  chown root:root "$1"
  chmod 750 "$1"
' sh "${gateway_symlink_target}"
gateway_symlink_target_before="$("${compose[@]}" exec -T cpa-manager-plus stat -c '%u:%g:%a' "${gateway_symlink_target}")"
"${compose[@]}" exec -T --user 10001:10001 cpa-manager-plus sh -ec '
  rmdir "$1/gateway/auth"
  ln -s "$2" "$1/gateway/auth"
' sh "${rt_root}" "${gateway_symlink_target}"
assert_runtime_bootstrap_fails_closed "Gateway state path is not a real directory: ${rt_root}/gateway/auth"
"${compose[@]}" stop cpa-manager-plus >/dev/null 2>&1 || true
gateway_symlink_target_after="$("${compose[@]}" run --rm --no-deps --entrypoint sh cpa-manager-plus -ec '
  stat -c "%u:%g:%a" "$1"
' sh "${gateway_symlink_target}")"
[ "${gateway_symlink_target_after}" = "${gateway_symlink_target_before}" ] ||
  fail "Gateway symlink target changed from ${gateway_symlink_target_before} to ${gateway_symlink_target_after}"
"${compose[@]}" run --rm --no-deps --entrypoint sh cpa-manager-plus -ec '
  test -L "$1/gateway/auth"
  rm "$1/gateway/auth"
  mkdir "$1/gateway/auth"
  chown 10001:10001 "$1/gateway/auth"
  chmod 700 "$1/gateway/auth"
' sh "${rt_root}" >/dev/null
"${compose[@]}" up "${up_args[@]}" --force-recreate cpa-manager-plus >/dev/null
wait_ready "${artifact_a}" >/dev/null || fail "Runtime did not recover after repairing Gateway auth directory"
resolve_public_origin
assert_runtime_token_preserved "after Gateway auth repair recreate"
pid_after_gateway_repair="$(runtime_cpa_pid)"
[ "$(cpa_exe_path "${pid_after_gateway_repair}")" = "${rt_root}/supervisor/artifacts/cpa/8.0.55/cli-proxy-api" ] ||
  fail "recreated CPA after Gateway auth repair did not execute finalized 8.0.55 stage"
assert_cpa_identity "${pid_after_gateway_repair}"
assert_secret_and_private_state_denied "${pid_after_gateway_repair}"
assert_execution_corridor "${pid_after_gateway_repair}" 8.0.55
[ "$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 "${public_origin}/v1/models")" = "401" ] || fail "unauthenticated /v1/models accepted after recreate"
assert_client_models_ok "after Gateway auth repair recreate"

"${compose[@]}" exec -T cpa-manager-plus sh -ec '
  chown -R 10001:10001 "$1/supervisor/artifacts/cpa/8.0.55"
  test "$(stat -c %a "$1/supervisor/artifacts/cpa/8.0.55")" = 510
  test "$(stat -c %a "$1/supervisor/artifacts/cpa/8.0.55/cli-proxy-api")" = 755
  test "$(stat -c %a "$1/supervisor/artifacts/cpa/8.0.55/artifact.json")" = 444
  test "$(stat -c %a "$1/supervisor/artifacts/cpa/8.0.55/LICENSE")" = 444
' sh "${rt_root}"
assert_runtime_bootstrap_fails_closed "finalized stage directory: persisted state owner is not the Supervisor: owner UID 10001 differs from Supervisor UID 0"

echo "Runtime18 secret/state E2E passed: CPA=10001:10001 pinned=8.0.55(${artifact_a}) D=${artifact_d} rollback=${failure_code} destination=config-v8"
