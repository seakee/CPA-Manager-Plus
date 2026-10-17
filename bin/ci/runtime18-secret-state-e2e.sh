#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
project="${CPAMP_RUNTIME18_PROJECT:-cpamp-runtime18-${RANDOM}-$$}"
public_port="${CPAMP_RUNTIME18_PORT:-31317}"
skip_build="${CPAMP_RUNTIME18_SKIP_BUILD:-false}"
initializer_name="${project}-cpamp-manager-init"
compose=(docker compose --project-directory "${repo_root}" -p "${project}")

cleanup() {
  docker rm -f "${initializer_name}" >/dev/null 2>&1 || true
  "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
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

runtime_get() {
  local request_path="$1"
  "${compose[@]}" exec -T cpamp-manager sh -ec '
    token="$(cat /run/cpamp/runtime-secret/token)"
    wget -qO- -T 5 \
      --header="Authorization: Bearer ${token}" \
      --header="X-CPAMP-Runtime-Features: active-gateway-artifact-v1" \
      "http://cpamp-runtime:9081${1}"
  ' sh "${request_path}"
}

runtime_activate() {
  local payload="$1"
  "${compose[@]}" exec -T cpamp-manager sh -ec '
    token="$(cat /run/cpamp/runtime-secret/token)"
    wget -qO- -T 170 \
      --header="Authorization: Bearer ${token}" \
      --header="Content-Type: application/json" \
      --post-data="${1}" \
      http://cpamp-runtime:9081/v1/runtime/operations/activate-update
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

assert_cpa_identity() {
  local pid="$1"
  local actual_uid actual_gid actual_groups expected_uid expected_gid
  actual_uid="$("${compose[@]}" exec -T cpamp-runtime awk '$1 == "Uid:" { print $2; exit }' "/proc/${pid}/status")"
  actual_gid="$("${compose[@]}" exec -T cpamp-runtime awk '$1 == "Gid:" { print $2; exit }' "/proc/${pid}/status")"
  expected_uid="$("${compose[@]}" exec -T cpamp-runtime sh -ec 'printf %s "$CPAMP_RUNTIME_CPA_UID"')"
  expected_gid="$("${compose[@]}" exec -T cpamp-runtime sh -ec 'printf %s "$CPAMP_RUNTIME_CPA_GID"')"
  actual_groups="$("${compose[@]}" exec -T cpamp-runtime awk '$1 == "Groups:" { $1 = ""; sub(/^ +/, ""); print; exit }' "/proc/${pid}/status")"
  [ "${actual_uid}" = "${expected_uid}" ] || fail "CPA PID ${pid} UID=${actual_uid}, want ${expected_uid}"
  [ "${actual_gid}" = "${expected_gid}" ] || fail "CPA PID ${pid} GID=${actual_gid}, want ${expected_gid}"
  [ "${actual_groups}" = "${expected_gid}" ] || fail "CPA PID ${pid} supplementary groups=${actual_groups}, want only ${expected_gid}"
  [ "${actual_uid}" != "0" ] && [ "${actual_gid}" != "0" ] || fail "CPA PID ${pid} retained root identity"
}

assert_gateway_writable() {
  local pid="$1"
  local uid gid
  uid="$("${compose[@]}" exec -T cpamp-runtime awk '$1 == "Uid:" { print $2; exit }' "/proc/${pid}/status")"
  gid="$("${compose[@]}" exec -T cpamp-runtime awk '$1 == "Gid:" { print $2; exit }' "/proc/${pid}/status")"
  "${compose[@]}" exec -T --user "${uid}:${gid}" cpamp-runtime sh -ec '
    test -r /runtime/gateway/config.yaml
    test -w /runtime/gateway
    marker=/runtime/gateway/.runtime18-cpa-write
    printf runtime18 >"${marker}"
    test "$(cat "${marker}")" = runtime18
    rm "${marker}"
  '
}

assert_secret_and_private_state_denied() {
  local pid="$1"
  local uid gid
  uid="$("${compose[@]}" exec -T cpamp-runtime awk '$1 == "Uid:" { print $2; exit }' "/proc/${pid}/status")"
  gid="$("${compose[@]}" exec -T cpamp-runtime awk '$1 == "Gid:" { print $2; exit }' "/proc/${pid}/status")"
  if "${compose[@]}" exec -T --user "${uid}:${gid}" cpamp-runtime cat /run/cpamp/runtime-secret/token >/dev/null 2>&1; then
    fail "CPA identity can read the Runtime transport secret"
  fi
  if "${compose[@]}" exec -T --user "${uid}:${gid}" cpamp-runtime ls /runtime/supervisor >/dev/null 2>&1; then
    fail "CPA identity can enumerate Supervisor state"
  fi
  if "${compose[@]}" exec -T --user "${uid}:${gid}" cpamp-runtime cat /runtime/supervisor/operations.sqlite >/dev/null 2>&1; then
    fail "CPA identity can read the Supervisor journal"
  fi
  if "${compose[@]}" exec -T --user "${uid}:${gid}" cpamp-runtime cat /runtime/supervisor/active/cpa/selection.json >/dev/null 2>&1; then
    fail "CPA identity can read active selection"
  fi
  if "${compose[@]}" exec -T --user "${uid}:${gid}" cpamp-runtime cat /runtime/supervisor/artifacts/cpa/.runtime18-private/marker >/dev/null 2>&1; then
    fail "CPA identity can enter a Supervisor-private staging workspace"
  fi
  if "${compose[@]}" exec -T --user "${uid}:${gid}" cpamp-runtime sh -ec 'printf x >>/runtime/supervisor/operations.sqlite' >/dev/null 2>&1; then
    fail "CPA identity can mutate Supervisor private state"
  fi
  if "${compose[@]}" exec -T --user "${uid}:${gid}" cpamp-runtime sh -ec "tr '\000' '\n' </proc/${pid}/environ" |
    grep -Eq '^(CPAMP_RUNTIME_|CPAMP_CPA_EXECUTABLE=|CPAMP_CPA_ARTIFACT_MANIFEST=)'; then
    fail "CPA environment contains Supervisor-private Runtime configuration"
  fi
}

assert_mount_isolation() {
  "${compose[@]}" exec -T cpamp-manager sh -ec '
    test ! -e /runtime
    test -r /run/cpamp/runtime-secret/token
    ! touch /run/cpamp/runtime-secret/.runtime18-write-probe
  ' >/dev/null 2>&1 || fail "Manager mount isolation or read-only token contract changed"
  "${compose[@]}" exec -T cpamp-ingress sh -ec '
    test ! -e /data
    test ! -e /runtime
    test ! -e /run/cpamp/runtime-secret
  ' || fail "Ingress gained Manager, Runtime, or token storage"
}

assert_execution_corridor() {
  local pid="$1"
  local version="$2"
  local uid gid executable
  uid="$("${compose[@]}" exec -T cpamp-runtime awk '$1 == "Uid:" { print $2; exit }' "/proc/${pid}/status")"
  gid="$("${compose[@]}" exec -T cpamp-runtime awk '$1 == "Gid:" { print $2; exit }' "/proc/${pid}/status")"
  executable="/runtime/supervisor/artifacts/cpa/${version}/cli-proxy-api"
  "${compose[@]}" exec -T --user "${uid}:${gid}" cpamp-runtime test -r "${executable}" || fail "CPA cannot read finalized ${version} executable"
  "${compose[@]}" exec -T --user "${uid}:${gid}" cpamp-runtime test -x "${executable}" || fail "CPA cannot execute finalized ${version} executable"
  if "${compose[@]}" exec -T --user "${uid}:${gid}" cpamp-runtime ls /runtime/supervisor/artifacts/cpa >/dev/null 2>&1; then
    fail "artifact execution corridor permits stage enumeration"
  fi
}

seed_stage() {
  local version="$1"
  local source="$2"
  local trailer="$3"
  "${compose[@]}" exec -T cpamp-runtime sh -ec '
    version="$1"
    source="$2"
    trailer="$3"
    cpa_gid="${CPAMP_RUNTIME_CPA_GID:?}"
    directory="/runtime/supervisor/artifacts/cpa/${version}"
    mkdir -p "${directory}"
    chmod 700 "${directory}"
    cp "${source}" "${directory}/cli-proxy-api"
    if [ -n "${trailer}" ]; then printf "%s" "${trailer}" >>"${directory}/cli-proxy-api"; fi
    chmod 755 "${directory}/cli-proxy-api"
    digest="$(sha256sum "${directory}/cli-proxy-api")"
    digest="${digest%% *}"
    printf "{\"schemaVersion\":1,\"engine\":\"cpa\",\"version\":\"%s\",\"artifactId\":\"sha256:%s\",\"sourceArchiveDigest\":\"sha256:%064d\"}\n" \
      "${version}" "${digest}" 0 >"${directory}/artifact.json"
    chmod 444 "${directory}/artifact.json"
    chown root:"${cpa_gid}" "${directory}"
    chmod 510 "${directory}"
    printf "sha256:%s" "${digest}"
  ' sh "${version}" "${source}" "${trailer}"
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
  "${compose[@]}" up -d --force-recreate cpamp-runtime >/dev/null 2>&1 || true
  while [ "${count}" -lt 30 ]; do
    if runtime_cpa_pid >/dev/null 2>&1; then
      fail "untrusted persisted state started CPA"
    fi
    if "${compose[@]}" logs --no-color --tail 100 cpamp-runtime 2>&1 | grep -Fq "${expected_log}"; then
      if runtime_get /v1/runtime/status >/dev/null 2>&1; then
        fail "untrusted persisted state left Runtime Supervisor reachable"
      fi
      return 0
    fi
    count=$((count + 1))
    sleep 1
  done
  "${compose[@]}" logs --no-color --tail 100 cpamp-runtime >&2 || true
  fail "untrusted persisted state did not produce expected failure: ${expected_log}"
}

repair_persisted_owner_and_restart() {
  local persisted_path="$1"
  "${compose[@]}" stop cpamp-runtime >/dev/null 2>&1 || true
  "${compose[@]}" run --rm --no-deps --entrypoint sh cpamp-runtime -ec '
    chown root:root "$1"
  ' sh "${persisted_path}" >/dev/null
  "${compose[@]}" up -d --force-recreate cpamp-runtime >/dev/null
  wait_ready "${artifact_b}" >/dev/null || fail "Runtime did not recover after repairing ${persisted_path} owner"
}

cd "${repo_root}"
export CPAMP_PUBLIC_PORT="${public_port}"
if [ "${skip_build}" != "true" ]; then
  "${compose[@]}" build
fi
# This Runtime E2E starts from an explicit historical Manager lifecycle
# baseline. Fresh initialize remains one-shot and never gains Runtime authority.
"${repo_root}/bin/ci/runtime-lifecycle-manager-baseline.sh" "${project}" "${initializer_name}"
"${compose[@]}" up -d
public_origin="http://127.0.0.1:${public_port}"
retry 120 curl --fail --silent "${public_origin}/health" >/dev/null
retry 120 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null

"${compose[@]}" exec -T cpamp-runtime sh -ec '
  test "$(awk '\''$1 == "Uid:" { print $2; exit }'\'' /proc/1/status)" = 0
  mkdir -p /runtime/supervisor/artifacts/cpa/.runtime18-private
  chmod 700 /runtime/supervisor/artifacts/cpa/.runtime18-private
  printf private >/runtime/supervisor/artifacts/cpa/.runtime18-private/marker
  chmod 600 /runtime/supervisor/artifacts/cpa/.runtime18-private/marker
'

status_a="$(runtime_get /v1/runtime/status)"
artifact_a="$(printf '%s' "${status_a}" | json_field activeGatewayArtifact.artifactId)"
generation_a="$(printf '%s' "${status_a}" | json_uint_field runtimeGeneration)"
pid_a="$(runtime_cpa_pid)"
assert_cpa_identity "${pid_a}"
assert_gateway_writable "${pid_a}"
assert_secret_and_private_state_denied "${pid_a}"
assert_mount_isolation

artifact_b="$(seed_stage 7.3.4 /usr/local/bin/cli-proxy-api runtime18-candidate-b)"
[ "${artifact_b}" != "${artifact_a}" ] || fail "candidate B must differ from bundled A"
assert_execution_corridor "${pid_a}" 7.3.4
activate_b="$(activate runtime18-activate-b "${generation_a}" "${artifact_a}" 7.3.4)"
[ "$(printf '%s' "${activate_b}" | json_field state)" = succeeded ] || fail "A to B activation failed: ${activate_b}"
status_b="$(wait_ready "${artifact_b}")"
[ "$(printf '%s' "${status_b}" | json_field recovery.state)" = armed ] || fail "B recovery is not armed"
pid_b="$(runtime_cpa_pid)"
[ "${pid_b}" != "${pid_a}" ] || fail "activation did not replace A"
assert_cpa_identity "${pid_b}"
assert_secret_and_private_state_denied "${pid_b}"
assert_execution_corridor "${pid_b}" 7.3.4
retry 30 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null

"${compose[@]}" up -d --force-recreate cpamp-runtime >/dev/null
status_recreated="$(wait_ready "${artifact_b}")"
generation_b="$(printf '%s' "${status_recreated}" | json_uint_field runtimeGeneration)"
[ "${generation_b}" != "${generation_a}" ] || fail "Runtime recreation did not rotate generation"
pid_recreated="$(runtime_cpa_pid)"
assert_cpa_identity "${pid_recreated}"
assert_gateway_writable "${pid_recreated}"
assert_secret_and_private_state_denied "${pid_recreated}"
assert_execution_corridor "${pid_recreated}" 7.3.4
retry 30 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null

"${compose[@]}" exec -T cpamp-runtime sh -ec 'kill -9 "$1"' sh "${pid_recreated}"
pid_recovered="$(wait_recovery_replacement "${pid_recreated}" "${artifact_b}")"
assert_cpa_identity "${pid_recovered}"
assert_execution_corridor "${pid_recovered}" 7.3.4
retry 30 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null

artifact_d="$(seed_stage 7.3.6 /usr/local/bin/cli-proxy-api runtime18-inaccessible-d)"
"${compose[@]}" exec -T cpamp-runtime chmod 500 /runtime/supervisor/artifacts/cpa/7.3.6
activate_d="$(activate runtime18-activate-d "${generation_b}" "${artifact_b}" 7.3.6)"
[ "$(printf '%s' "${activate_d}" | json_field state)" = failed ] || fail "inaccessible D did not fail closed: ${activate_d}"
[ "$(printf '%s' "${activate_d}" | json_field error.code)" = activation_start_failed ] || fail "inaccessible D was not a spawn failure: ${activate_d}"
status_after_d="$(wait_ready "${artifact_b}")"
pid_after_d="$(runtime_cpa_pid)"
assert_cpa_identity "${pid_after_d}"
[ "$(printf '%s' "${status_after_d}" | json_field recovery.state)" = armed ] || fail "rollback after D did not arm B recovery"

artifact_c="$(seed_stage 7.3.5 /bin/true '')"
activate_c="$(activate runtime18-activate-c "${generation_b}" "${artifact_b}" 7.3.5)"
[ "$(printf '%s' "${activate_c}" | json_field state)" = failed ] || fail "unready C did not fail: ${activate_c}"
failure_code="$(printf '%s' "${activate_c}" | json_field error.code)"
case "${failure_code}" in
  activation_start_failed|activation_readiness_failed) ;;
  *) fail "unready C failure code is ${failure_code}" ;;
esac
status_after_c="$(wait_ready "${artifact_b}")"
[ "$(printf '%s' "${status_after_c}" | json_field recovery.state)" = armed ] || fail "rollback B recovery is not armed"
pid_after_c="$(runtime_cpa_pid)"
assert_cpa_identity "${pid_after_c}"
assert_secret_and_private_state_denied "${pid_after_c}"
assert_execution_corridor "${pid_after_c}" 7.3.4
assert_mount_isolation
retry 30 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null

"${compose[@]}" exec -T cpamp-runtime chown 10001:10001 /runtime/supervisor/active/cpa/selection.json
assert_runtime_bootstrap_fails_closed "selection file: persisted state owner is not the Supervisor: owner UID 10001 differs from Supervisor UID 0"
repair_persisted_owner_and_restart /runtime/supervisor/active/cpa/selection.json

"${compose[@]}" exec -T cpamp-runtime chown 10001:10001 /runtime/supervisor/operations.sqlite
assert_runtime_bootstrap_fails_closed "journal file: persisted state owner is not the Supervisor: owner UID 10001 differs from Supervisor UID 0"
repair_persisted_owner_and_restart /runtime/supervisor/operations.sqlite

"${compose[@]}" exec -T cpamp-runtime chown 10001:10001 /runtime/supervisor/active
assert_runtime_bootstrap_fails_closed "selection parent: persisted state owner is not the Supervisor: owner UID 10001 differs from Supervisor UID 0"
repair_persisted_owner_and_restart /runtime/supervisor/active

gateway_symlink_target=/runtime/supervisor/.runtime18-gateway-symlink-target
"${compose[@]}" exec -T cpamp-runtime sh -ec '
  mkdir "$1"
  chown root:root "$1"
  chmod 750 "$1"
' sh "${gateway_symlink_target}"
gateway_symlink_target_before="$("${compose[@]}" exec -T cpamp-runtime stat -c '%u:%g:%a' "${gateway_symlink_target}")"
"${compose[@]}" exec -T --user 10001:10001 cpamp-runtime sh -ec '
  rmdir /runtime/gateway/auth
  ln -s "$1" /runtime/gateway/auth
' sh "${gateway_symlink_target}"
assert_runtime_bootstrap_fails_closed "Gateway state path is not a real directory: /runtime/gateway/auth"
"${compose[@]}" stop cpamp-runtime >/dev/null 2>&1 || true
gateway_symlink_target_after="$("${compose[@]}" run --rm --no-deps --entrypoint sh cpamp-runtime -ec '
  stat -c "%u:%g:%a" "$1"
' sh "${gateway_symlink_target}")"
[ "${gateway_symlink_target_after}" = "${gateway_symlink_target_before}" ] ||
  fail "Gateway symlink target changed from ${gateway_symlink_target_before} to ${gateway_symlink_target_after}"
"${compose[@]}" run --rm --no-deps --entrypoint sh cpamp-runtime -ec '
  test -L /runtime/gateway/auth
  rm /runtime/gateway/auth
  mkdir /runtime/gateway/auth
  chown 10001:10001 /runtime/gateway/auth
  chmod 700 /runtime/gateway/auth
' >/dev/null
"${compose[@]}" up -d --force-recreate cpamp-runtime >/dev/null
wait_ready "${artifact_b}" >/dev/null || fail "Runtime did not recover after repairing Gateway auth directory"

"${compose[@]}" exec -T cpamp-runtime sh -ec '
  chown -R 10001:10001 /runtime/supervisor/artifacts/cpa/7.3.4
  test "$(stat -c %a /runtime/supervisor/artifacts/cpa/7.3.4)" = 510
  test "$(stat -c %a /runtime/supervisor/artifacts/cpa/7.3.4/cli-proxy-api)" = 755
  test "$(stat -c %a /runtime/supervisor/artifacts/cpa/7.3.4/artifact.json)" = 444
'
assert_runtime_bootstrap_fails_closed "finalized stage directory: persisted state owner is not the Supervisor: owner UID 10001 differs from Supervisor UID 0"

echo "Runtime18 secret/state E2E passed: CPA=10001:10001 A=${artifact_a} B=${artifact_b} D=${artifact_d} rollback=${failure_code}"
