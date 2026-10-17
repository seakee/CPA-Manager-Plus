#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
project="${1:-}"
initializer_name="${2:-}"

if [ -z "${project}" ] || [ -z "${initializer_name}" ]; then
  echo "usage: runtime-lifecycle-manager-baseline.sh <compose-project> <initializer-container>" >&2
  exit 2
fi

compose=(docker compose --project-directory "${repo_root}" -p "${project}")
scratch_root="$(mktemp -d "${TMPDIR:-/tmp}/cpamp-runtime-manager-baseline.XXXXXX")"

cleanup() {
  docker rm -f "${initializer_name}" >/dev/null 2>&1 || true
  rm -rf "${scratch_root}"
}
trap cleanup EXIT HUP INT TERM

# Phase4-03 initialize is intentionally a one-shot Fresh admission/handoff
# command. Verify that exact contract first; never wait for a daemon or /health.
"${compose[@]}" run -d --no-deps --name "${initializer_name}" cpamp-manager initialize >/dev/null
exit_code="$(docker wait "${initializer_name}")"
if [ "${exit_code}" != "0" ]; then
  echo "Fresh initializer exited with status ${exit_code}" >&2
  docker logs "${initializer_name}" >&2 || true
  exit 1
fi

handoff="$(docker logs "${initializer_name}" 2>/dev/null | tail -n 1)"
case "${handoff}" in
  "/.well-known/cpamp-bootstrap/ui#handoff="*) ;;
  *)
    echo "Fresh initializer did not emit the expected fixed Bootstrap fragment handoff" >&2
    exit 1
    ;;
esac

mkdir -p "${scratch_root}/data"
docker cp "${initializer_name}:/data/." "${scratch_root}/data"
test -s "${scratch_root}/data/usage.sqlite"
test -s "${scratch_root}/data/data.key"

# Runtime12/14/17/18/19 exercise the pre-Phase4 Runtime lifecycle contract,
# not the new Fresh product workflow. Convert only this disposable CI volume
# into an explicit historical Manager baseline. This must never become product
# initialization behavior: Fresh ownership is removed, an old bootstrap-state
# witness is installed, and normal Manager startup then creates the historical
# Embedded Runtime desired state through its existing compatibility path.
python3 - "${scratch_root}/data/usage.sqlite" <<'PY'
import json
import sqlite3
import sys
import time

path = sys.argv[1]
connection = sqlite3.connect(path)
try:
    keys = {row[0] for row in connection.execute("select key from settings")}
    required = {"fresh_operation_v1", "fresh_authority_v1"}
    missing = required - keys
    if missing:
        raise SystemExit(f"Fresh initializer baseline is missing ownership settings: {sorted(missing)}")

    connection.execute(
        "delete from settings where key in "
        "('fresh_operation_v1','fresh_authority_v1','admin_credential_v1',"
        "'manager_config_v1','setup','runtime_desired_v1','bootstrap_state_v1')"
    )
    now_ms = int(time.time() * 1000)
    state = {
        "version": 1,
        "status": "needs_setup",
        "adminReady": False,
        "projectInitialized": False,
        "dataKeyReady": True,
        "migratedLegacy": True,
        "hasHistoricalData": False,
        "updatedAtMs": now_ms,
        "connectionStorageMigrationVersion": 2,
    }
    connection.execute(
        "insert into settings(key,value,updated_at_ms) values(?,?,?)",
        ("bootstrap_state_v1", json.dumps(state, separators=(",", ":")), now_ms),
    )
    connection.commit()
    connection.execute("pragma wal_checkpoint(truncate)")

    remaining = {row[0] for row in connection.execute("select key from settings")}
    if "fresh_operation_v1" in remaining or "fresh_authority_v1" in remaining:
        raise SystemExit("Fresh ownership remained in Runtime lifecycle baseline")
    if "runtime_desired_v1" in remaining:
        raise SystemExit("Runtime desired state was pre-seeded instead of being created by Manager startup")
    if "bootstrap_state_v1" not in remaining:
        raise SystemExit("historical bootstrap baseline was not persisted")
finally:
    connection.close()
PY

docker cp "${scratch_root}/data/usage.sqlite" "${initializer_name}:/data/usage.sqlite"

# Fresh uses a non-empty same-inode receipt as ownership evidence. Historical
# Manager startup uses the existing empty retained marker. Remove any copied
# WAL sidecars and convert only the disposable test marker before startup.
"${compose[@]}" run --rm --no-deps --entrypoint sh cpamp-manager -ec '
  rm -f /data/usage.sqlite-wal /data/usage.sqlite-shm
  : > /data/usage.sqlite.manager.lock
  test -s /data/usage.sqlite
  test -s /data/data.key
  test -e /data/usage.sqlite.manager.lock
  test ! -s /data/usage.sqlite.manager.lock
'

docker rm "${initializer_name}" >/dev/null
trap - EXIT HUP INT TERM
rm -rf "${scratch_root}"
