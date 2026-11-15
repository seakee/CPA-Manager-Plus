#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# The Dockerfiles fail closed on identity; bind a local build to this checkout.
export VERSION="${VERSION:-dev}"
export SOURCE_COMMIT="${SOURCE_COMMIT:-$(git -C "${repo_root}" rev-parse HEAD)}"
project="${CPAMP_RUNTIME17_PROJECT:-${CPAMP_SMOKE_PROJECT:-cpamp-runtime17-${RANDOM}-$$}}"
public_port="${CPAMP_RUNTIME17_PORT:-${CPAMP_SMOKE_PORT:-${CPAMP_PUBLIC_PORT:-30317}}}"
skip_build="${CPAMP_RUNTIME17_SKIP_BUILD:-${CPAMP_SMOKE_SKIP_BUILD:-false}}"
initializer_name="${project}-cpamp-manager-init"
private_dir="$(mktemp -d "${TMPDIR:-/tmp}/cpamp-runtime17-priv.XXXXXX")"
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
bundled_hold_path="/usr/local/bin/cli-proxy-api.runtime17-bundled-hold"

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
  echo "Runtime17 activation E2E: $*" >&2
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

container_id() {
  local id
  id="$("${compose[@]}" ps --all -q cpa-manager-plus)"
  test -n "${id}"
  printf '%s' "${id}"
}

runtime_root() {
  "${compose[@]}" exec -T cpa-manager-plus sh -ec 'if [ -d /data/runtime ]; then printf /data/runtime; else printf /runtime; fi'
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

# Negative rollback fault-injection only: stages a non-CPA binary (such as
# /bin/true) under a non-v8 legacy version directory strictly to exercise
# ActivateUpdate failure and rollback to the genuine pinned CPA 8.0.55 stage.
seed_negative_fault_stage() {
  local version="$1"
  local source="$2"
  "${compose[@]}" exec -T cpa-manager-plus sh -ec '
    version="$1"
    source="$2"
    rt_root="/data/runtime"
    [ -d "${rt_root}" ] || rt_root="/runtime"
    directory="${rt_root}/supervisor/artifacts/cpa/${version}"
    cpa_gid="${CPAMP_RUNTIME_CPA_GID:-10001}"
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
  selected="$("${compose[@]}" exec -T cpa-manager-plus sh -ec 'rt_root="/data/runtime"; [ -d "${rt_root}" ] || rt_root="/runtime"; cat "${rt_root}/supervisor/active/cpa/selection.json"')"
  [ "$(printf '%s' "${selected}" | json_field schemaVersion)" = "1" ] || fail "selection schema is not v1"
  [ "$(printf '%s' "${selected}" | json_field engine)" = "cpa" ] || fail "selection engine is not cpa"
  [ "$(printf '%s' "${selected}" | json_field version)" = "${expected_version}" ] || fail "selection version changed"
  [ "$(printf '%s' "${selected}" | json_field artifactId)" = "${expected_artifact}" ] || fail "selection artifact changed"
  if printf '%s' "${selected}" | grep -q 'path\|url\|command'; then
    fail "selection persisted forbidden execution authority"
  fi
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
bound_port="${public_port##*:}"
if [ "${bound_port}" = "0" ]; then
  bound_port="$("${compose[@]}" port cpa-manager-plus 18317)"
  bound_port="${bound_port##*:}"
fi
public_origin="http://127.0.0.1:${bound_port}"
retry 120 curl --fail --silent "${public_origin}/health" >/dev/null
retry 120 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null

rt_root="$(runtime_root)"
status_a="$(runtime_get /v1/runtime/status)"
artifact_a="$(printf '%s' "${status_a}" | json_field activeGatewayArtifact.artifactId)"
[ "$(printf '%s' "${status_a}" | json_field activeGatewayArtifact.version)" = "8.0.55" ] || fail "bundled A version is not 8.0.55"
[ "$(printf '%s' "${status_a}" | json_field cpaObservedVersion)" = "8.0.55" ] || fail "bundled A cpaObservedVersion is not 8.0.55"
generation_a="$(printf '%s' "${status_a}" | json_uint_field runtimeGeneration)"
pid_a="$(runtime_cpa_pid)"
[[ "${artifact_a}" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "bundled A artifact is not canonical"
[ "$(cpa_exe_path "${pid_a}")" = "/usr/local/bin/cli-proxy-api" ] || fail "initial CPA did not execute bundled binary"
"${compose[@]}" exec -T cpa-manager-plus test ! -e "${rt_root}/supervisor/active/cpa/selection.json"

# Pre-stage admission guard: activate-update before 8.0.55 is staged fails closed with 409 target_stage_unavailable.
cid="$(container_id)"
missing_stage_resp="$(
  printf '{"operationId":"runtime17-missing-stage","expectedRuntimeIdentity":"embedded-default","expectedRuntimeGeneration":%s,"expectedActiveArtifactId":"%s","targetVersion":"8.0.55"}' \
    "${generation_a}" "${artifact_a}" |
    post_runtime_json "${cid}" "/v1/runtime/operations/activate-update"
)"
[ "$(printf '%s' "${missing_stage_resp}" | json_field status)" = "409" ] || fail "missing stage did not return 409"
[ "$(printf '%s' "${missing_stage_resp}" | json_field body.error.code)" = "target_stage_unavailable" ] || fail "missing stage error code mismatch"
[ "$(runtime_cpa_pid)" = "${pid_a}" ] || fail "rejected activate-update disturbed bundled child A"

# Note: positive A -> B activate-update requires two distinct valid CPA binaries
# (activate_update.go enforces candidate.ArtifactID != prior.ArtifactID). Under
# the single pinned CPA 8.0.55 binary without downloading or forging binaries,
# transition to finalized_stage + selection.json via the real Supervisor
# PUT /v1/runtime/artifact/upload API, verify finalized stage execution overrides
# the restored bundled binary, and verify same-artifact activate-update returns
# 409 operation_state_conflict.
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
generation_selected="$(printf '%s' "${status_unbundled}" | json_uint_field runtimeGeneration)"
uploaded_artifact="$(python3 "${repo_root}/bin/ci/runtime-v8-upload-stage.py" upload-pinned \
  "${cid}" "${bundled_hold_path}" "${rt_root}")"
[ "${uploaded_artifact}" = "${artifact_a}" ] || fail "uploaded artifact digest ${uploaded_artifact} != ${artifact_a}"
"${compose[@]}" exec -T cpa-manager-plus sh -ec '
  mv "$1" /usr/local/bin/cli-proxy-api
  test -x /usr/local/bin/cli-proxy-api
  kill -CONT "$2"
' sh "${bundled_hold_path}" "${paused_manager_pid}"
paused_manager_pid=""

status_selected="$(wait_ready "${artifact_a}" 120)"
[ "$(printf '%s' "${status_selected}" | json_uint_field runtimeGeneration)" = "${generation_selected}" ] || fail "selected stage start changed RuntimeGeneration"
[ "$(printf '%s' "${status_selected}" | json_field activeGatewayArtifact.version)" = "8.0.55" ] || fail "selected stage version is not 8.0.55"
[ "$(printf '%s' "${status_selected}" | json_field recovery.state)" = "armed" ] || fail "selected 8.0.55 recovery is not armed"
pid_selected="$(runtime_cpa_pid)"
[ "${pid_selected}" != "${pid_a}" ] || fail "selected stage start did not spawn a new CPA process"
[ "$(cpa_exe_path "${pid_selected}")" = "${rt_root}/supervisor/artifacts/cpa/8.0.55/cli-proxy-api" ] || fail "selected CPA did not execute finalized 8.0.55 stage"
retry 30 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null
assert_selection 8.0.55 "${artifact_a}"

# Same-artifact conflict guard: activate-update to 8.0.55 when 8.0.55 is already active returns 409 operation_state_conflict.
same_artifact_resp="$(
  printf '{"operationId":"runtime17-same-artifact","expectedRuntimeIdentity":"embedded-default","expectedRuntimeGeneration":%s,"expectedActiveArtifactId":"%s","targetVersion":"8.0.55"}' \
    "${generation_selected}" "${artifact_a}" |
    post_runtime_json "${cid}" "/v1/runtime/operations/activate-update"
)"
[ "$(printf '%s' "${same_artifact_resp}" | json_field status)" = "409" ] || fail "same-artifact activate-update did not return 409"
[ "$(printf '%s' "${same_artifact_resp}" | json_field body.error.code)" = "operation_state_conflict" ] || fail "same-artifact error code mismatch"
[ "$(runtime_cpa_pid)" = "${pid_selected}" ] || fail "same-artifact conflict disturbed running CPA child"
assert_selection 8.0.55 "${artifact_a}"

"${compose[@]}" exec -T cpa-manager-plus sh -ec 'kill -TERM $(pidof cpamp-runtime-supervisor)'
status_b_recreated="$(wait_ready "${artifact_a}" 120)"
generation_b="$(printf '%s' "${status_b_recreated}" | json_uint_field runtimeGeneration)"
[ "${generation_b}" != "${generation_selected}" ] || fail "Runtime recreation did not rotate generation"
pid_b_recreated="$(runtime_cpa_pid)"
[ "$(cpa_exe_path "${pid_b_recreated}")" = "${rt_root}/supervisor/artifacts/cpa/8.0.55/cli-proxy-api" ] || fail "recreated Supervisor did not retain finalized 8.0.55 stage"
retry 30 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null
assert_selection 8.0.55 "${artifact_a}"

artifact_c="$(seed_negative_fault_stage 7.3.5 /bin/true)"
[ "${artifact_c}" != "${artifact_a}" ] || fail "fault stage C unexpectedly matched CPA 8.0.55 digest"
activate_c_payload="$(printf '{"operationId":"runtime17-activate-c","expectedRuntimeIdentity":"embedded-default","expectedRuntimeGeneration":%s,"expectedActiveArtifactId":"%s","targetVersion":"7.3.5"}' "${generation_b}" "${artifact_a}")"
activate_c="$(runtime_activate "${activate_c_payload}")"
[ "$(printf '%s' "${activate_c}" | json_field state)" = "failed" ] || fail "unready C activation did not fail (state=$(printf '%s' "${activate_c}" | json_field state))"
failure_code="$(printf '%s' "${activate_c}" | json_field error.code)"
case "${failure_code}" in
  activation_start_failed|activation_readiness_failed) ;;
  *) fail "unready C failure code is ${failure_code}" ;;
esac
"${compose[@]}" exec -T cpa-manager-plus sh -ec 'chmod 700 "$1" && rm -rf "$1"' sh "${rt_root}/supervisor/artifacts/cpa/7.3.5"
status_after_rollback="$(wait_ready "${artifact_a}" 120)"
[ "$(printf '%s' "${status_after_rollback}" | json_field recovery.state)" = "armed" ] || fail "rollback 8.0.55 recovery is not armed"
pid_after_rollback="$(runtime_cpa_pid)"
[ "$(cpa_exe_path "${pid_after_rollback}")" = "${rt_root}/supervisor/artifacts/cpa/8.0.55/cli-proxy-api" ] || fail "rollback did not respawn finalized 8.0.55 stage"
retry 30 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null
assert_selection 8.0.55 "${artifact_a}"

generation_before_second_recreate="$(printf '%s' "${status_after_rollback}" | json_uint_field runtimeGeneration)"
"${compose[@]}" exec -T cpa-manager-plus sh -ec 'kill -TERM $(pidof cpamp-runtime-supervisor)'
status_after_second_recreate="$(wait_ready "${artifact_a}" 120)"
generation_after_second_recreate="$(printf '%s' "${status_after_second_recreate}" | json_uint_field runtimeGeneration)"
[ "${generation_after_second_recreate}" != "${generation_before_second_recreate}" ] || fail "second Runtime recreation did not rotate generation"
pid_after_second_recreate="$(runtime_cpa_pid)"
[ "$(cpa_exe_path "${pid_after_second_recreate}")" = "${rt_root}/supervisor/artifacts/cpa/8.0.55/cli-proxy-api" ] || fail "second recreation did not retain finalized 8.0.55 stage"
retry 30 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null
assert_selection 8.0.55 "${artifact_a}"

echo "Runtime17 activation E2E passed: pinned=8.0.55(${artifact_a}) stageUpload=acquired samePinGuard=operation_state_conflict rollback=${failure_code}"
