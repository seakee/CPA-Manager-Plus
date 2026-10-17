#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
project="${CPAMP_RUNTIME17_PROJECT:-cpamp-runtime17-${RANDOM}-$$}"
public_port="${CPAMP_RUNTIME17_PORT:-30317}"
skip_build="${CPAMP_RUNTIME17_SKIP_BUILD:-false}"
initializer_name="${project}-cpamp-manager-init"
compose=(docker compose --project-directory "${repo_root}" -p "${project}")

cleanup() {
  docker rm -f "${initializer_name}" >/dev/null 2>&1 || true
  "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
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

seed_stage() {
  local version="$1"
  local source="$2"
  local trailer="$3"
  "${compose[@]}" exec -T cpamp-runtime sh -ec '
    version="$1"
    source="$2"
    trailer="$3"
    directory="/runtime/supervisor/artifacts/cpa/${version}"
    cpa_gid="${CPAMP_RUNTIME_CPA_GID:?}"
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

assert_selection() {
  local expected_version="$1"
  local expected_artifact="$2"
  local selected
  selected="$("${compose[@]}" exec -T cpamp-runtime cat /runtime/supervisor/active/cpa/selection.json)"
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
# This Runtime E2E starts from an explicit historical Manager lifecycle
# baseline. Fresh read-only discovery and one-shot initialize are covered by
# Runtime12/the shared helper and must not implicitly select Embedded Runtime.
"${repo_root}/bin/ci/runtime-lifecycle-manager-baseline.sh" "${project}" "${initializer_name}"
"${compose[@]}" up -d
public_origin="http://127.0.0.1:${public_port}"
retry 120 curl --fail --silent "${public_origin}/health" >/dev/null
retry 120 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null

status_a="$(runtime_get /v1/runtime/status)"
artifact_a="$(printf '%s' "${status_a}" | json_field activeGatewayArtifact.artifactId)"
generation_a="$(printf '%s' "${status_a}" | json_uint_field runtimeGeneration)"
pid_a="$(runtime_cpa_pid)"
[[ "${artifact_a}" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "bundled A artifact is not canonical"

artifact_b="$(seed_stage 7.3.4 /usr/local/bin/cli-proxy-api runtime17-candidate-b)"
[ "${artifact_b}" != "${artifact_a}" ] || fail "candidate B must differ from bundled A"
activate_b_payload="$(printf '{"operationId":"runtime17-activate-b","expectedRuntimeIdentity":"embedded-default","expectedRuntimeGeneration":%s,"expectedActiveArtifactId":"%s","targetVersion":"7.3.4"}' "${generation_a}" "${artifact_a}")"
activate_b="$(runtime_activate "${activate_b_payload}")"
[ "$(printf '%s' "${activate_b}" | json_field state)" = "succeeded" ] || fail "A to B activation did not succeed: ${activate_b}"

status_b="$(wait_ready "${artifact_b}" 120)"
[ "$(printf '%s' "${status_b}" | json_uint_field runtimeGeneration)" = "${generation_a}" ] || fail "activation changed RuntimeGeneration"
[ "$(printf '%s' "${status_b}" | json_field recovery.state)" = "armed" ] || fail "B recovery is not armed"
pid_b="$(runtime_cpa_pid)"
[ "${pid_b}" != "${pid_a}" ] || fail "activation did not replace exact child A"
retry 30 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null
assert_selection 7.3.4 "${artifact_b}"

"${compose[@]}" up -d --force-recreate cpamp-runtime >/dev/null
status_b_recreated="$(wait_ready "${artifact_b}" 120)"
generation_b="$(printf '%s' "${status_b_recreated}" | json_uint_field runtimeGeneration)"
[ "${generation_b}" != "${generation_a}" ] || fail "Runtime recreation did not rotate generation"
retry 30 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null
assert_selection 7.3.4 "${artifact_b}"

artifact_c="$(seed_stage 7.3.5 /bin/true '')"
activate_c_payload="$(printf '{"operationId":"runtime17-activate-c","expectedRuntimeIdentity":"embedded-default","expectedRuntimeGeneration":%s,"expectedActiveArtifactId":"%s","targetVersion":"7.3.5"}' "${generation_b}" "${artifact_b}")"
activate_c="$(runtime_activate "${activate_c_payload}")"
[ "$(printf '%s' "${activate_c}" | json_field state)" = "failed" ] || fail "unready C activation did not fail: ${activate_c}"
failure_code="$(printf '%s' "${activate_c}" | json_field error.code)"
case "${failure_code}" in
  activation_start_failed|activation_readiness_failed) ;;
  *) fail "unready C failure code is ${failure_code}" ;;
esac
status_after_rollback="$(wait_ready "${artifact_b}" 120)"
[ "$(printf '%s' "${status_after_rollback}" | json_field recovery.state)" = "armed" ] || fail "rollback B recovery is not armed"
retry 30 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null
assert_selection 7.3.4 "${artifact_b}"

generation_before_second_recreate="$(printf '%s' "${status_after_rollback}" | json_uint_field runtimeGeneration)"
"${compose[@]}" up -d --force-recreate cpamp-runtime >/dev/null
status_after_second_recreate="$(wait_ready "${artifact_b}" 120)"
generation_after_second_recreate="$(printf '%s' "${status_after_second_recreate}" | json_uint_field runtimeGeneration)"
[ "${generation_after_second_recreate}" != "${generation_before_second_recreate}" ] || fail "second Runtime recreation did not rotate generation"
retry 30 curl --fail --silent --show-error "${public_origin}/v1/models" >/dev/null
assert_selection 7.3.4 "${artifact_b}"

echo "Runtime17 activation E2E passed: A=${artifact_a} B=${artifact_b} rollback=${failure_code}"
