#!/usr/bin/env bash
set -euo pipefail
umask 077

app_name="cpa-manager-plus"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

resolve_path() {
  local path="$1"
  if command -v realpath >/dev/null 2>&1; then
    realpath "${path}" 2>/dev/null || printf '%s\n' "${path}"
    return
  fi
  if command -v readlink >/dev/null 2>&1; then
    readlink -f "${path}" 2>/dev/null || printf '%s\n' "${path}"
    return
  fi

  if [ -d "${path}" ]; then
    (
      cd "${path}" 2>/dev/null && pwd -P
    ) || printf '%s\n' "${path}"
    return
  fi

  (
    cd "$(dirname "${path}")" 2>/dev/null && printf '%s/%s\n' "$(pwd -P)" "$(basename "${path}")"
  ) || printf '%s\n' "${path}"
}

if [ -n "${CPA_MANAGER_PLUS_HOME:-}" ]; then
  home_dir="$(resolve_path "${CPA_MANAGER_PLUS_HOME}")"
elif [ "$(basename "${script_dir}")" = "bin" ] && {
  [ -e "${script_dir}/cpa-manager-plus" ] ||
    [ -e "${script_dir}/../cpa-manager-plusctl" ] ||
    [ -e "${script_dir}/../cpa-manager-plus.service" ]
}; then
  home_dir="$(cd "${script_dir}/.." && pwd -P)"
else
  home_dir="${script_dir}"
fi

default_data_dir="${home_dir}/data"
default_run_dir="${home_dir}/run"
default_log_dir="${home_dir}/logs"
binary="${CPA_MANAGER_PLUS_BIN:-"${home_dir}/${app_name}"}"
data_dir="${USAGE_DATA_DIR:-"${CPA_MANAGER_PLUS_DATA_DIR:-"${default_data_dir}"}"}"
run_dir="${CPA_MANAGER_PLUS_RUN_DIR:-"${default_run_dir}"}"
log_dir="${CPA_MANAGER_PLUS_LOG_DIR:-"${default_log_dir}"}"
pid_file="${CPA_MANAGER_PLUS_PID_FILE:-"${run_dir}/${app_name}.pid"}"
log_file="${CPA_MANAGER_PLUS_LOG_FILE:-"${log_dir}/${app_name}.log"}"
manage_data_dir="false"
manage_run_dir="false"
manage_log_dir="false"

if [ -z "${USAGE_DATA_DIR:-}" ] && [ -z "${CPA_MANAGER_PLUS_DATA_DIR:-}" ]; then
  manage_data_dir="true"
fi
if [ -z "${CPA_MANAGER_PLUS_RUN_DIR:-}" ]; then
  manage_run_dir="true"
fi
if [ -z "${CPA_MANAGER_PLUS_LOG_DIR:-}" ]; then
  manage_log_dir="true"
fi

bin_dir="${CPA_MANAGER_PLUS_BIN_DIR:-"${home_dir}/bin"}"
runtime_supervisor_bin="${CPA_MANAGER_PLUS_RUNTIME_SUPERVISOR_BIN:-"${bin_dir}/cpa-manager-plus-runtime-supervisor"}"
manager_bin="${CPA_MANAGER_PLUS_MANAGER_BIN:-"${CPA_MANAGER_PLUS_BIN:-"${bin_dir}/cpa-manager-plus"}"}"
ingress_bin="${CPA_MANAGER_PLUS_INGRESS_BIN:-"${bin_dir}/cpa-manager-plus-ingress"}"
cpa_bin="${CPAMP_CPA_EXECUTABLE:-"${bin_dir}/cli-proxy-api"}"

resolve_launcher_bin() {
  local candidate
  for candidate in \
    "${CPA_MANAGER_PLUS_LAUNCHER:-}" \
    "${bin_dir}/cpa-manager-plus-launcher" \
    "${home_dir}/cpa-manager-plus-launcher" \
    "${script_dir}/cpa-manager-plus-launcher" \
    "${script_dir}/bin/cpa-manager-plus-launcher"; do
    if [ -n "${candidate}" ] && [ -f "${candidate}" ] && [ -x "${candidate}" ]; then
      resolve_path "${candidate}"
      return 0
    fi
  done
  return 1
}

is_v2_mode() {
  if [ "${CPAMP_NATIVE_MODE:-}" = "v1" ]; then
    return 1
  fi
  if [ "${CPAMP_NATIVE_MODE:-}" = "v2" ] ||
    [ -n "${CPA_MANAGER_PLUS_LAUNCHER:-}" ] ||
    [ -n "${CPA_MANAGER_PLUS_RUNTIME_SUPERVISOR_BIN:-}" ] ||
    [ -n "${CPA_MANAGER_PLUS_INGRESS_BIN:-}" ] ||
    [ -n "${CPA_MANAGER_PLUS_MANAGER_BIN:-}" ]; then
    return 0
  fi
  if [ -z "${CPA_MANAGER_PLUS_BIN:-}" ] &&
    [ -x "${runtime_supervisor_bin}" ] &&
    [ -x "${manager_bin}" ] &&
    [ -x "${ingress_bin}" ]; then
    return 0
  fi
  if [ -f "${run_dir}/launcher.ready" ] ||
    [ -f "${run_dir}/runtime-supervisor.pid" ] ||
    [ -f "${run_dir}/manager.pid" ] ||
    [ -f "${run_dir}/ingress.pid" ] ||
    [ -f "${run_dir}/cpa-instance.provenance" ]; then
    return 0
  fi
  return 1
}

record_format=""
record_pid=""
record_start=""
record_binary=""
record_command=""
record_state=""

usage() {
  cat <<EOF
Usage: $(basename "$0") <command> [args...]

Commands:
  install [opts]   Install systemd unit and initialize v2 native directories
  upgrade [opts]   Atomically upgrade an existing v2 native installation
  start [args...]  Start cpa-manager-plus in the background
  stop             Stop the background process(es)
  restart          Restart the background process(es)
  status           Show process status
  logs [lines|-f|--follow]
                   Print recent logs, or follow with -f/--follow

install options:
  --unit-dir DIR        systemd unit directory, default: /etc/systemd/system
  --service-name NAME   Unit name, default: cpa-manager-plus.service
  --home DIR            Installation root, default: ${home_dir}
  --no-systemctl        Write the unit without daemon-reload/enable

upgrade options:
  --home DIR            Target installation root, default: /opt/cpa-manager-plus
  --service-name NAME   Unit name, default: cpa-manager-plus.service
  --unit-dir DIR        systemd unit directory, default: /etc/systemd/system
  --no-systemctl        Skip systemctl stop/daemon-reload/enable/start
  --no-restart          Do not restart the service after upgrading

install creates the dedicated unprivileged CPA identity (CPAMP_CPA_USER,
default cpamp-cpa) when absent, sets the Docker-equivalent ownership layout,
and writes a hardened unit. The launcher itself must run as root so the
Runtime Supervisor can drop only CPA; --user/--group are therefore rejected.
Environment overrides:
  CPA_MANAGER_PLUS_HOME      Installation root directory
  CPA_MANAGER_PLUS_BIN       Binary path
  CPA_MANAGER_PLUS_RUN_DIR   Runtime directory, default: ./run
  CPA_MANAGER_PLUS_LOG_DIR   Log directory, default: ./logs
  CPA_MANAGER_PLUS_PID_FILE  PID file path
  CPA_MANAGER_PLUS_LOG_FILE  Log file path
  CPAMP_CPA_USER             Dedicated CPA account name, default: cpamp-cpa
  CPAMP_RUNTIME_CPA_UID      CPA child UID (must be paired with GID)
  CPAMP_RUNTIME_CPA_GID      CPA child GID
EOF
}

reset_record() {
  record_format=""
  record_pid=""
  record_start=""
  record_binary=""
  record_command=""
  record_state=""
}

read_pid_record_from() {
  local target_pid_file="$1"
  reset_record

  if [ ! -f "${target_pid_file}" ]; then
    return 1
  fi

  reject_unsafe_runtime_file "${target_pid_file}"
  reject_unsafe_runtime_file_parent "${target_pid_file}"

  local line saw_metadata=0
  while IFS= read -r line || [ -n "${line}" ]; do
    case "${line}" in
      pid=*)
        record_pid="${line#pid=}"
        saw_metadata=1
        ;;
      start=*)
        record_start="${line#start=}"
        saw_metadata=1
        ;;
      binary=*)
        record_binary="${line#binary=}"
        saw_metadata=1
        ;;
      command=*)
        record_command="${line#command=}"
        saw_metadata=1
        ;;
      '')
        ;;
      *)
        if [ "${saw_metadata}" -eq 0 ] && [[ "${line}" =~ ^[0-9]+$ ]]; then
          record_pid="${line}"
          record_format="legacy"
        fi
        ;;
    esac
  done <"${target_pid_file}"

  if ! [[ "${record_pid}" =~ ^[0-9]+$ ]]; then
    return 1
  fi

  if [ "${saw_metadata}" -eq 1 ]; then
    record_format="metadata"
  fi

  return 0
}

read_pid_record() {
  read_pid_record_from "${pid_file}"
}

is_pid_running() {
  local pid="$1"
  kill -0 "${pid}" >/dev/null 2>&1
}

process_start_marker() {
  local pid="$1"

  if [ -r "/proc/${pid}/stat" ]; then
    awk '{print $22}' "/proc/${pid}/stat" 2>/dev/null
    return
  fi

  ps -ww -p "${pid}" -o lstart= 2>/dev/null | awk '{$1=$1;print}'
}

process_executable_path() {
  local pid="$1"
  local resolved_process
  if [ -L "/proc/${pid}/exe" ]; then
    resolved_process="$(resolve_path "/proc/${pid}/exe")"
    [ -n "${resolved_process}" ] && printf '%s\n' "${resolved_process}"
    return
  fi

  if command -v lsof >/dev/null 2>&1; then
    lsof -a -p "${pid}" -d txt -Fn 2>/dev/null | sed -n 's/^n//p' | head -n 1
    return
  fi
}

process_command_line() {
  local pid="$1"
  ps -ww -p "${pid}" -o command= 2>/dev/null | sed 's/^[[:space:]]*//'
}

set_pid_record_state_for() {
  local target_pid_file="$1"
  local current_binary current_command current_start

  if [ -L "${target_pid_file}" ]; then
    echo "Refusing to use symlinked runtime file: ${target_pid_file}" >&2
    exit 1
  fi

  if [ ! -f "${target_pid_file}" ]; then
    if [ -e "${target_pid_file}" ]; then
      echo "Refusing to use non-regular runtime file: ${target_pid_file}" >&2
      exit 1
    fi
    record_state="missing"
    return
  fi

  if ! read_pid_record_from "${target_pid_file}"; then
    record_state="invalid"
    return
  fi

  if ! is_pid_running "${record_pid}"; then
    record_state="stale"
    return
  fi

  if [ "${record_format}" != "metadata" ] || [ -z "${record_start}" ]; then
    record_state="conflict"
    return
  fi

  current_start="$(process_start_marker "${record_pid}")"
  if [ -z "${current_start}" ] || [ "${current_start}" != "${record_start}" ]; then
    record_state="conflict"
    return
  fi

  current_binary="$(process_executable_path "${record_pid}" || true)"
  if [ -n "${current_binary}" ] && [ -n "${record_binary}" ]; then
    if [ "${current_binary}" = "${record_binary}" ]; then
      record_state="active"
      return
    fi

    record_state="conflict"
    return
  fi

  current_command="$(process_command_line "${record_pid}")"
  if [ -n "${current_command}" ] && [ -n "${record_command}" ] && [ "${current_command}" = "${record_command}" ]; then
    record_state="active"
    return
  fi

  record_state="conflict"
}

set_pid_record_state() {
  set_pid_record_state_for "${pid_file}"
}

inspect_instance_pid() {
  local target_pid_file="$1"
  local saved_format="${record_format}"
  local saved_pid="${record_pid}"
  local saved_start="${record_start}"
  local saved_binary="${record_binary}"
  local saved_command="${record_command}"
  local saved_state="${record_state}"
  local mapped_state="conflict"

  set_pid_record_state_for "${target_pid_file}"
  case "${record_state}" in
    missing) mapped_state="absent" ;;
    active) mapped_state="running" ;;
    stale) mapped_state="stale" ;;
    invalid) mapped_state="invalid" ;;
    conflict) mapped_state="conflict" ;;
    *) mapped_state="conflict" ;;
  esac

  record_format="${saved_format}"
  record_pid="${saved_pid}"
  record_start="${saved_start}"
  record_binary="${saved_binary}"
  record_command="${saved_command}"
  record_state="${saved_state}"

  printf '%s\n' "${mapped_state}"
}

running_pid() {
  set_pid_record_state
  if [ "${record_state}" = "active" ]; then
    printf '%s\n' "${record_pid}"
    return 0
  fi

  return 1
}

recorded_instance_running() {
  local current_start

  if ! read_pid_record; then
    return 1
  fi

  if [ "${record_format}" != "metadata" ] || [ -z "${record_start}" ]; then
    return 1
  fi

  if ! is_pid_running "${record_pid}"; then
    return 1
  fi

  current_start="$(process_start_marker "${record_pid}")"
  [ -n "${current_start}" ] && [ "${current_start}" = "${record_start}" ]
}

ensure_private_dir() {
  local dir="$1"
  local manage_existing="${2:-false}"

  if [ -z "${dir}" ]; then
    return 0
  fi

  if [ "${dir}" = "." ]; then
    dir="$(pwd -P)"
  fi

  if [ -d "${dir}" ]; then
    if [ -L "${dir}" ]; then
      echo "Refusing to use symlinked runtime directory: ${dir}" >&2
      exit 1
    fi

    if [ "${manage_existing}" = "true" ]; then
      chmod 700 "${dir}"
    else
      reject_unsafe_custom_dir "${dir}"
    fi
    return 0
  fi

  (umask 077 && mkdir -p "${dir}")
  chmod 700 "${dir}"
}

path_mode() {
  local path="$1"

  if stat -c '%a' "${path}" >/dev/null 2>&1; then
    stat -c '%a' "${path}"
    return
  fi

  stat -f '%Lp' "${path}"
}

path_owner_and_mode() {
  local path="$1"

  if stat -c '%u:%g:%a' "${path}" >/dev/null 2>&1; then
    stat -c '%u:%g:%a' "${path}"
    return 0
  fi

  stat -f '%u:%g:%Lp' "${path}"
}

is_group_or_world_writable_mode() {
  local mode="$1"
  local perms group other

  perms="${mode}"
  while [ "${#perms}" -lt 3 ]; do
    perms="0${perms}"
  done
  perms="${perms: -3}"
  group="${perms:1:1}"
  other="${perms:2:1}"

  [ $((10#${group} & 2)) -ne 0 ] || [ $((10#${other} & 2)) -ne 0 ]
}

reject_unsafe_custom_dir() {
  local dir="$1"
  local mode

  mode="$(path_mode "${dir}")"
  if is_group_or_world_writable_mode "${mode}"; then
    echo "Refusing to use unsafe runtime directory: ${dir} must not be group- or world-writable." >&2
    exit 1
  fi
}

reject_unsafe_runtime_file() {
  local file="$1"

  if [ -L "${file}" ]; then
    echo "Refusing to use symlinked runtime file: ${file}" >&2
    exit 1
  fi

  if [ -e "${file}" ] && [ ! -f "${file}" ]; then
    echo "Refusing to use non-regular runtime file: ${file}" >&2
    exit 1
  fi
}

# `gateway_dir` belongs to the unprivileged CPA identity once installed. Root
# must never follow a path inside it: a planted symlink would let the child
# redirect a privileged chown or chmod onto any root-owned file.
reject_symlinks_under() {
  local dir="$1"
  local found

  [ -d "${dir}" ] || return 0
  found="$(find "${dir}" -type l -print 2>/dev/null | sed -n '1p')"
  if [ -n "${found}" ]; then
    echo "Refusing to continue: ${found} is a symlink inside ${dir}, which belongs to the CPA identity" >&2
    exit 1
  fi
}

is_managed_runtime_dir() {
  local dir="$1"

  { [ "${dir}" = "${run_dir}" ] && [ "${manage_run_dir}" = "true" ]; } ||
    { [ "${dir}" = "${log_dir}" ] && [ "${manage_log_dir}" = "true" ]; } ||
    { [ "${dir}" = "${data_dir}" ] && [ "${manage_data_dir}" = "true" ]; }
}

reject_unsafe_runtime_file_parent() {
  local file="$1"
  local parent_dir

  parent_dir="$(dirname "${file}")"
  if [ -z "${parent_dir}" ] || [ ! -e "${parent_dir}" ]; then
    return 0
  fi

  if [ -L "${parent_dir}" ]; then
    echo "Refusing to use symlinked runtime directory: ${parent_dir}" >&2
    exit 1
  fi

  if [ ! -d "${parent_dir}" ]; then
    echo "Refusing to use non-directory runtime path: ${parent_dir}" >&2
    exit 1
  fi

  if ! is_managed_runtime_dir "${parent_dir}"; then
    reject_unsafe_custom_dir "${parent_dir}"
  fi
}

prepare_private_file() {
  local file="$1"
  local parent_dir

  reject_unsafe_runtime_file "${file}"
  parent_dir="$(dirname "${file}")"
  if [ "${parent_dir}" = "${run_dir}" ]; then
    ensure_private_dir "${parent_dir}" "${manage_run_dir}"
  elif [ "${parent_dir}" = "${log_dir}" ]; then
    ensure_private_dir "${parent_dir}" "${manage_log_dir}"
  elif [ "${parent_dir}" = "${data_dir}" ]; then
    ensure_private_dir "${parent_dir}" "${manage_data_dir}"
  else
    ensure_private_dir "${parent_dir}" "false"
  fi

  (umask 077 && touch "${file}")
  chmod 600 "${file}"
}

prepare_runtime_paths() {
  ensure_private_dir "${run_dir}" "${manage_run_dir}"
  ensure_private_dir "${log_dir}" "${manage_log_dir}"
  prepare_private_file "${log_file}"
}

write_pid_record() {
  local pid="$1"
  local current_binary current_command current_start tmp_file

  current_start="$(process_start_marker "${pid}")"
  current_binary="$(process_executable_path "${pid}" || true)"
  current_command="$(process_command_line "${pid}")"

  if [ -z "${current_start}" ] || { [ -z "${current_binary}" ] && [ -z "${current_command}" ]; }; then
    return 1
  fi

  tmp_file="${pid_file}.tmp.$$"
  prepare_private_file "${tmp_file}"
  {
    printf 'pid=%s\n' "${pid}"
    printf 'start=%s\n' "${current_start}"
    printf 'binary=%s\n' "${current_binary}"
    printf 'command=%s\n' "${current_command}"
  } >"${tmp_file}"
  chmod 600 "${tmp_file}"
  mv -f "${tmp_file}" "${pid_file}"
  chmod 600 "${pid_file}"
}

child_pid_if_active() {
  local child_file="$1"
  local saved_format="${record_format}"
  local saved_pid="${record_pid}"
  local saved_start="${record_start}"
  local saved_binary="${record_binary}"
  local saved_command="${record_command}"
  local saved_state="${record_state}"
  local active_pid=""

  set_pid_record_state_for "${child_file}"
  if [ "${record_state}" = "active" ]; then
    active_pid="${record_pid}"
  fi

  record_format="${saved_format}"
  record_pid="${saved_pid}"
  record_start="${saved_start}"
  record_binary="${saved_binary}"
  record_command="${saved_command}"
  record_state="${saved_state}"

  if [ -n "${active_pid}" ]; then
    printf '%s\n' "${active_pid}"
    return 0
  fi
  return 1
}

all_v2_children_active() {
  child_pid_if_active "${run_dir}/runtime-supervisor.pid" >/dev/null 2>&1 &&
    child_pid_if_active "${run_dir}/manager.pid" >/dev/null 2>&1 &&
    child_pid_if_active "${run_dir}/ingress.pid" >/dev/null 2>&1
}

# The Supervisor never stops its CPA child (its Close contract leaves that to a
# container runtime) and a crashed launcher cannot either, so `cleanup_v2_children`
# would otherwise leave cli-proxy-api holding the CPA port and the next start
# would fail with "cpa-gateway port ... already bound".
expected_cpa_uid() {
  local cpa_user="${CPAMP_CPA_USER:-cpamp-cpa}"

  if [ -n "${CPAMP_RUNTIME_CPA_UID:-}" ]; then
    printf '%s\n' "${CPAMP_RUNTIME_CPA_UID}"
    return 0
  fi
  if [ "$(id -u)" -eq 0 ] && [ "$(uname -s)" = "Linux" ]; then
    if [[ "${cpa_user}" =~ ^[a-z_][a-z0-9_-]*$ ]] && id -u "${cpa_user}" >/dev/null 2>&1; then
      id -u "${cpa_user}"
    else
      printf '10001\n'
    fi
    return 0
  fi
  id -u
}

listening_pids_on_port() {
  local port="$1"
  local hex_port inodes inode pid fd target file

  if [ -d /proc/net ]; then
    hex_port="$(printf '%04X' "$((10#${port}))")"
    inodes="$(
      for file in /proc/net/tcp /proc/net/tcp6; do
        [ -r "${file}" ] || continue
        awk -v wanted="${hex_port}" '
          NR > 1 && $4 == "0A" && $10 != "0" && $10 != "" {
            count = split($2, parts, ":")
            if (count >= 2 && parts[count] == wanted) print $10
          }
        ' "${file}"
      done
    )"
    [ -n "${inodes}" ] || return 0
    for pid in /proc/[0-9]*; do
      pid="${pid#/proc/}"
      [ -d "/proc/${pid}/fd" ] || continue
      for fd in "/proc/${pid}/fd/"*; do
        [ -L "${fd}" ] || continue
        target="$(readlink "${fd}" 2>/dev/null || true)"
        case "${target}" in
          socket:\[*\])
            inode="${target#socket:[}"
            inode="${inode%]}"
            ;;
          *) continue ;;
        esac
        if printf '%s\n' "${inodes}" | grep -qx "${inode}"; then
          printf '%s\n' "${pid}"
          break
        fi
      done
    done
    return 0
  fi

  if command -v lsof >/dev/null 2>&1; then
    lsof -nP -a -iTCP:"${port}" -sTCP:LISTEN -t 2>/dev/null | sort -u || true
  fi
}

# Instance-scoped provenance for the CPA child, mirroring the launcher. The
# Supervisor spawns CPA with `-config <runtime_root>/.../config.yaml` once a
# destination is staged and otherwise inherits the Gateway directory as its
# working directory, so both the entry point and the runtime configuration lie
# inside this installation. A process that only listens on the CPA port, or only
# carries a matching argv substring under the same UID, is untrusted and is left
# untouched for the operator.
#
# The launcher-written record pins a pid and start time; it is advisory. `ctl`
# decides attribution from live process state (identity, entry point, runtime
# configuration) and only accepts a record whose pinned start time still matches
# the live process, so a stale or forged file can never widen what is signalled.
cpa_runtime_root() {
  local runtime_data_dir="${CPAMP_RUNTIME_DATA_DIR:-"${data_dir}/runtime"}"
  local journal_path="${CPAMP_RUNTIME_JOURNAL_PATH:-"${runtime_data_dir}/supervisor/operations.sqlite"}"

  dirname "$(dirname "${journal_path}")"
}

cpa_supervisor_dir() {
  local runtime_data_dir="${CPAMP_RUNTIME_DATA_DIR:-"${data_dir}/runtime"}"
  local journal_path="${CPAMP_RUNTIME_JOURNAL_PATH:-"${runtime_data_dir}/supervisor/operations.sqlite"}"

  dirname "${journal_path}"
}

is_instance_cpa_executable_path() {
  local candidate_path="$1"
  local primary_cpa_bin="$2"
  local sup_dir="$3"
  local stage_prefix rel_path seg

  [ -n "${candidate_path}" ] || return 1
  if [ -n "${primary_cpa_bin}" ] && [ "${candidate_path}" = "${primary_cpa_bin}" ]; then
    return 0
  fi

  [ -n "${sup_dir}" ] || return 1
  while [ "${sup_dir}" != "/" ] && [ "${sup_dir%/}" != "${sup_dir}" ]; do
    sup_dir="${sup_dir%/}"
  done
  stage_prefix="${sup_dir}/artifacts/cpa/"
  case "${candidate_path}" in
    "${stage_prefix}"*/cli-proxy-api)
      rel_path="${candidate_path#"${stage_prefix}"}"
      seg="${rel_path%/cli-proxy-api}"
      [ -n "${seg}" ] || return 1
      case "${seg}" in
        */* | . | .. | *..*) return 1 ;;
      esac
      return 0
      ;;
  esac
  return 1
}

cpa_provenance_file() {
  printf '%s\n' "${run_dir}/cpa-instance.provenance"
}

process_stat_field() {
  local pid="$1"
  local field="$2"
  local line

  [ -r "/proc/${pid}/stat" ] || return 1
  line="$(sed 's/^[0-9]* (.*) //' "/proc/${pid}/stat" 2>/dev/null || true)"
  [ -n "${line}" ] || return 1
  printf '%s\n' "${line}" | awk -v field="${field}" '{print $field}'
}

process_parent_pid() {
  local pid="$1"
  local parent=""

  parent="$(process_stat_field "${pid}" 2 || true)"
  if [ -n "${parent}" ]; then
    printf '%s\n' "${parent}"
    return 0
  fi
  ps -o ppid= -p "${pid}" 2>/dev/null | tr -d ' ' || true
}

process_is_descendant_of() {
  local pid="$1"
  local ancestor="$2"
  local current="${pid}"
  local depth=0

  [ -n "${ancestor}" ] || return 1
  while [ -n "${current}" ] && [ "${depth}" -lt 32 ]; do
    if [ "${current}" = "${ancestor}" ]; then
      return 0
    fi
    case "${current}" in
      0 | 1) return 1 ;;
    esac
    current="$(process_parent_pid "${current}" || true)"
    depth=$((depth + 1))
  done
  return 1
}

process_argv_tokens() {
  local pid="$1"

  if [ -r "/proc/${pid}/cmdline" ]; then
    tr '\0' '\n' <"/proc/${pid}/cmdline" 2>/dev/null || true
    return 0
  fi
  ps -ww -p "${pid}" -o args= 2>/dev/null | tr ' ' '\n'
}

process_working_directory() {
  local pid="$1"
  local cwd=""

  if [ -L "/proc/${pid}/cwd" ]; then
    cwd="$(resolve_path "/proc/${pid}/cwd")"
    [ -n "${cwd}" ] && printf '%s\n' "${cwd}"
    return 0
  fi
  if command -v lsof >/dev/null 2>&1; then
    lsof -a -p "${pid}" -d cwd -Fn 2>/dev/null | sed -n 's/^n//p' | head -n 1
  fi
}

cpa_provenance_record_start() {
  local provenance_file="$1"
  local wanted="$2"
  local record_pid record_start record_rest

  [ -f "${provenance_file}" ] || return 1
  while IFS='|' read -r record_pid record_start record_rest; do
    case "${record_pid}" in
      '' | '#'* | instance) continue ;;
    esac
    if [ "${record_pid}" = "${wanted}" ]; then
      printf '%s\n' "${record_start}"
      return 0
    fi
  done <"${provenance_file}"
  return 1
}

cpa_process_matches_instance() {
  local pid="$1"
  local runtime_root="$2"
  local supervisor_dir="${3:-$(cpa_supervisor_dir)}"
  local owner exe cwd token previous
  local entry_ok=0
  local config_ok=0

  [ -n "${runtime_root}" ] || return 1
  [ -n "${pid}" ] || return 1
  [ "${pid}" = "$$" ] && return 1
  is_pid_running "${pid}" || return 1

  owner="$(ps -o uid= -p "${pid}" 2>/dev/null | tr -d ' ' || true)"
  [ -n "${owner}" ] && [ "${owner}" = "$(expected_cpa_uid)" ] || return 1

  exe="$(process_executable_path "${pid}" || true)"
  if [ -n "${exe}" ] && is_instance_cpa_executable_path "${exe}" "${cpa_bin}" "${supervisor_dir}"; then
    entry_ok=1
  fi

  previous=""
  while IFS= read -r token; do
    if [ "${entry_ok}" -eq 0 ] && is_instance_cpa_executable_path "${token}" "${cpa_bin}" "${supervisor_dir}"; then
      entry_ok=1
    fi
    case "${token}" in
      -config=*)
        case "${token#-config=}" in
          "${runtime_root}"/*) config_ok=1 ;;
        esac
        ;;
      *)
        if [ "${previous}" = "-config" ]; then
          case "${token}" in
            "${runtime_root}"/*) config_ok=1 ;;
          esac
        fi
        ;;
    esac
    previous="${token}"
  done <<<"$(process_argv_tokens "${pid}")"

  if [ "${config_ok}" -eq 0 ]; then
    cwd="$(process_working_directory "${pid}" || true)"
    case "${cwd}" in
      "${runtime_root}" | "${runtime_root}"/*) config_ok=1 ;;
    esac
  fi

  [ "${entry_ok}" -eq 1 ] && [ "${config_ok}" -eq 1 ]
}

# Attribution requires the instance tuple *and* a pin written by the launcher, or
# a live place in the running Supervisor's process tree. Without one of those the
# process is not this installation's child and must not be signalled.
cpa_process_is_instance_owned() {
  local pid="$1"
  local runtime_root="$2"
  local provenance_file="$3"
  local supervisor_dir="${4:-$(cpa_supervisor_dir)}"
  local recorded supervisor_pid

  cpa_process_matches_instance "${pid}" "${runtime_root}" "${supervisor_dir}" || return 1

  recorded="$(cpa_provenance_record_start "${provenance_file}" "${pid}" || true)"
  if [ -n "${recorded}" ]; then
    [ "${recorded}" = "$(process_start_marker "${pid}")" ]
    return $?
  fi

  supervisor_pid="$(child_pid_if_active "${run_dir}/runtime-supervisor.pid" 2>/dev/null || true)"
  process_is_descendant_of "${pid}" "${supervisor_pid}"
}

reap_orphaned_cpa_processes() {
  local address runtime_root supervisor_dir provenance_file stage_prefix=""
  local port candidates pid pids="" i alive

  address="$(resolve_cpa_address)"
  runtime_root="$(cpa_runtime_root)"
  supervisor_dir="$(cpa_supervisor_dir)"
  provenance_file="$(cpa_provenance_file)"
  if [ -n "${supervisor_dir}" ]; then
    stage_prefix="${supervisor_dir%/}/artifacts/cpa/"
  fi

  port="$(printf '%s\n' "${address}" | sed -n 's/.*://p')"
  case "${port}" in
    '' | *[!0-9]*) return 0 ;;
  esac

  candidates="$(
    listening_pids_on_port "${port}"
    # The entry point travels through the environment: as an awk operand it
    # would match the command line of the collector itself.
    ps -ww -eo pid=,args= 2>/dev/null |
      CPAMP_CPA_ENTRY_POINT="${cpa_bin}" CPAMP_CPA_STAGE_PREFIX="${stage_prefix}" awk '
        (ENVIRON["CPAMP_CPA_ENTRY_POINT"] != "" && index($0, ENVIRON["CPAMP_CPA_ENTRY_POINT"]) > 0) ||
        (ENVIRON["CPAMP_CPA_STAGE_PREFIX"] != "" && index($0, ENVIRON["CPAMP_CPA_STAGE_PREFIX"]) > 0) {
          print $1
        }
      '
  )"

  for pid in $(printf '%s\n' "${candidates}" | sed '/^[[:space:]]*$/d' | sort -u); do
    [ "${pid}" = "$$" ] && continue
    is_pid_running "${pid}" || continue
    if ! cpa_process_is_instance_owned "${pid}" "${runtime_root}" "${provenance_file}" "${supervisor_dir}"; then
      echo "Refusing to signal PID ${pid} on ${address}: it cannot be attributed to this installation; operator intervention required" >&2
      continue
    fi
    echo "Stopping orphaned CPA child (PID ${pid}) on ${address}" >&2
    kill -TERM "${pid}" >/dev/null 2>&1 || true
    pids="${pids}${pid}"$'\n'
  done

  [ -n "${pids}" ] || return 0
  i=0
  while [ "${i}" -lt 50 ]; do
    alive=0
    for pid in ${pids}; do
      if is_pid_running "${pid}"; then
        alive=1
      fi
    done
    [ "${alive}" -eq 0 ] && return 0
    sleep 0.1
    i=$((i + 1))
  done
  for pid in ${pids}; do
    if is_pid_running "${pid}"; then
      # Re-validate the same identity on the same PID: a reused PID must never
      # receive this escalation.
      if ! cpa_process_is_instance_owned "${pid}" "${runtime_root}" "${provenance_file}" "${supervisor_dir}"; then
        echo "Refusing to SIGKILL PID ${pid} on ${address}: its instance provenance no longer matches" >&2
        continue
      fi
      echo "Orphaned CPA child (PID ${pid}) did not exit; sending SIGKILL" >&2
      kill -KILL "${pid}" >/dev/null 2>&1 || true
    fi
  done
}

_collect_attributable_cpa_candidates() {
  local port="$1"
  local inst_cpa_bin="$2"
  local inst_provenance_file="$3"
  local inst_supervisor_dir="${4:-}"
  local stage_prefix=""
  local record_pid record_start record_rest

  if [ -n "${inst_supervisor_dir}" ]; then
    stage_prefix="${inst_supervisor_dir%/}/artifacts/cpa/"
  fi
  if [ -n "${port}" ]; then
    listening_pids_on_port "${port}"
  fi
  ps -ww -eo pid=,args= 2>/dev/null |
    CPAMP_CPA_ENTRY_POINT="${inst_cpa_bin}" CPAMP_CPA_STAGE_PREFIX="${stage_prefix}" awk '
      (ENVIRON["CPAMP_CPA_ENTRY_POINT"] != "" && index($0, ENVIRON["CPAMP_CPA_ENTRY_POINT"]) > 0) ||
      (ENVIRON["CPAMP_CPA_STAGE_PREFIX"] != "" && index($0, ENVIRON["CPAMP_CPA_STAGE_PREFIX"]) > 0) {
        print $1
      }
    '
  if [ -f "${inst_provenance_file}" ] && [ ! -L "${inst_provenance_file}" ]; then
    while IFS='|' read -r record_pid record_start record_rest; do
      case "${record_pid}" in
        '' | '#'* | instance) continue ;;
      esac
      printf '%s\n' "${record_pid}"
    done <"${inst_provenance_file}"
  fi
}

find_attributable_cpa_pids_for_instance() {
  local inst_run_dir="$1"
  local inst_data_dir="$2"
  local inst_cpa_bin="$3"
  local inst_cpa_addr="${4:-}"
  local inst_cpa_uid="${5:-}"
  local inst_runtime_data_dir="${CPAMP_RUNTIME_DATA_DIR:-"${inst_data_dir}/runtime"}"
  local inst_journal_path="${CPAMP_RUNTIME_JOURNAL_PATH:-"${inst_runtime_data_dir}/supervisor/operations.sqlite"}"
  local inst_supervisor_dir inst_runtime_root
  inst_supervisor_dir="$(dirname "${inst_journal_path}")"
  inst_runtime_root="$(dirname "${inst_supervisor_dir}")"
  local inst_provenance_file="${inst_run_dir}/cpa-instance.provenance"
  local address="" port="" candidates="" pid

  if [ -f "${inst_run_dir}/cpa.address" ] && [ ! -L "${inst_run_dir}/cpa.address" ]; then
    address="$(sed -n '1p' "${inst_run_dir}/cpa.address" 2>/dev/null | tr -d '[:space:]' || true)"
  fi
  if [ -z "${address}" ] || ! split_host_port "${address}" >/dev/null 2>&1; then
    if [ -n "${inst_cpa_addr}" ]; then
      address="${inst_cpa_addr}"
    else
      address="$(cpa_address_from_environment)"
    fi
  fi

  port="$(printf '%s\n' "${address}" | sed -n 's/.*://p')"
  case "${port}" in
    '' | *[!0-9]*) port="" ;;
  esac

  candidates="$(_collect_attributable_cpa_candidates "${port}" "${inst_cpa_bin}" "${inst_provenance_file}" "${inst_supervisor_dir}")"

  (
    run_dir="${inst_run_dir}"
    data_dir="${inst_data_dir}"
    cpa_bin="${inst_cpa_bin}"
    if [ -n "${inst_cpa_uid}" ]; then
      export CPAMP_RUNTIME_CPA_UID="${inst_cpa_uid}"
    fi
    for pid in $(printf '%s\n' "${candidates}" | sed '/^[[:space:]]*$/d' | sort -u); do
      [ "${pid}" = "$$" ] && continue
      is_pid_running "${pid}" || continue
      if cpa_process_is_instance_owned "${pid}" "${inst_runtime_root}" "${inst_provenance_file}" "${inst_supervisor_dir}"; then
        printf '%s\n' "${pid}"
      fi
    done
  )
}

cleanup_v2_children() {
  local child_name child_file child_pid
  for child_name in ingress manager runtime-supervisor; do
    child_file="${run_dir}/${child_name}.pid"
    if child_pid="$(child_pid_if_active "${child_file}" 2>/dev/null)"; then
      kill -TERM "${child_pid}" >/dev/null 2>&1 || true
      local i=0
      while [ "${i}" -lt 50 ]; do
        if ! is_pid_running "${child_pid}"; then
          break
        fi
        sleep 0.1
        i=$((i + 1))
      done
      if is_pid_running "${child_pid}"; then
        kill -KILL "${child_pid}" >/dev/null 2>&1 || true
      fi
    fi
    rm -f "${child_file}"
  done
  reap_orphaned_cpa_processes
  rm -f "${run_dir}/launcher.ready" "${run_dir}/launcher.drain"
}

start_v2_app() {
  local launcher_bin
  if ! launcher_bin="$(resolve_launcher_bin)"; then
    echo "Launcher is not executable: ${bin_dir}/cpa-manager-plus-launcher" >&2
    exit 1
  fi

  set_pid_record_state
  case "${record_state}" in
    active)
      if all_v2_children_active; then
        echo "${app_name} is already running with PID ${record_pid}"
        return 0
      fi
      stop_app >/dev/null 2>&1 || true
      ;;
    missing)
      ;;
    stale | invalid)
      rm -f "${pid_file}"
      ;;
    conflict)
      echo "Refusing to start: ${pid_file} points to a running process that could not be strongly verified." >&2
      exit 1
      ;;
  esac

  prepare_runtime_paths
  if [ "$(dirname "${pid_file}")" = "${run_dir}" ]; then
    ensure_private_dir "${run_dir}" "${manage_run_dir}"
  else
    ensure_private_dir "$(dirname "${pid_file}")" "false"
  fi

  cleanup_v2_children
  rm -f "${pid_file}"

  local pid
  (
    cd "${home_dir}"
    export CPA_MANAGER_PLUS_HOME="${home_dir}"
    export CPA_MANAGER_PLUS_RUN_DIR="${run_dir}"
    export CPA_MANAGER_PLUS_LOG_DIR="${log_dir}"
    export CPA_MANAGER_PLUS_PID_FILE="${pid_file}"
    export CPA_MANAGER_PLUS_LOG_FILE="${log_file}"
    # Hand the launcher the address this installation resolves, so `start` and
    # `stop` always agree and neither falls back to a hardcoded 8317.
    launcher_cpa_address="${CPAMP_RUNTIME_CPA_ADDR:-}"
    if [ -z "${launcher_cpa_address}" ]; then
      launcher_cpa_address="$(resolve_cpa_address_for_instance "${run_dir}")"
    fi
    export CPAMP_RUNTIME_CPA_ADDR="${launcher_cpa_address}"
    export CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK=1
    unset CPAMP_UPGRADE_LOCK_TOKEN
    exec nohup "${launcher_bin}" "$@"
  ) >>"${log_file}" 2>&1 &
  pid="$!"

  local timeout_sec="${CPAMP_STARTUP_TIMEOUT_SEC:-30}"
  local max_ticks=$((timeout_sec * 10))
  local tick=0
  while [ "${tick}" -lt "${max_ticks}" ]; do
    if ! is_pid_running "${pid}"; then
      break
    fi
    if [ -f "${run_dir}/launcher.ready" ] && all_v2_children_active; then
      if write_pid_record "${pid}" && pid="$(running_pid)"; then
        local r_pid m_pid i_pid
        r_pid="$(child_pid_if_active "${run_dir}/runtime-supervisor.pid")"
        m_pid="$(child_pid_if_active "${run_dir}/manager.pid")"
        i_pid="$(child_pid_if_active "${run_dir}/ingress.pid")"
        echo "${app_name} started with PID ${pid}"
        echo "Components: runtime-supervisor (PID ${r_pid}), manager (PID ${m_pid}), ingress (PID ${i_pid})"
        echo "Log: ${log_file}"
        return 0
      fi
    fi
    sleep 0.1
    tick=$((tick + 1))
  done

  kill "${pid}" >/dev/null 2>&1 || true
  cleanup_v2_children
  rm -f "${pid_file}"
  echo "${app_name} failed to start. Recent log output:" >&2
  if [ -f "${log_file}" ]; then
    tail -n 40 "${log_file}" >&2
  fi
  exit 1
}

start_app() {
  ensure_no_active_upgrade_lock "${home_dir}" "start"
  if is_v2_mode; then
    start_v2_app "$@"
    return 0
  fi

  local launch_binary="${binary}"
  if [[ "${launch_binary}" != /* ]]; then
    launch_binary="$(resolve_path "${launch_binary}")"
  fi

  if [ ! -x "${launch_binary}" ]; then
    echo "Binary is not executable: ${binary}" >&2
    exit 1
  fi

  set_pid_record_state
  case "${record_state}" in
    active)
      echo "${app_name} is already running with PID ${record_pid}"
      return 0
      ;;
    missing)
      ;;
    stale | invalid)
      rm -f "${pid_file}"
      ;;
    conflict)
      echo "Refusing to start: ${pid_file} points to a running process that could not be strongly verified." >&2
      exit 1
      ;;
  esac

  prepare_runtime_paths
  if [ "$(dirname "${pid_file}")" = "${run_dir}" ]; then
    ensure_private_dir "${run_dir}" "${manage_run_dir}"
  else
    ensure_private_dir "$(dirname "${pid_file}")" "false"
  fi

  rm -f "${pid_file}"

  local pid
  (cd "${script_dir}" && exec nohup "${launch_binary}" "$@") >>"${log_file}" 2>&1 &
  pid="$!"

  sleep 1
  if write_pid_record "${pid}" && pid="$(running_pid)"; then
    echo "${app_name} started with PID ${pid}"
    echo "Log: ${log_file}"
    return 0
  fi

  kill "${pid}" >/dev/null 2>&1 || true
  rm -f "${pid_file}"
  echo "${app_name} failed to start. Recent log output:" >&2
  if [ -f "${log_file}" ]; then
    tail -n 40 "${log_file}" >&2
  fi
  exit 1
}

restart_app() {
  ensure_no_active_upgrade_lock "${home_dir}" "restart"
  stop_app
  start_app "$@"
}

_parsed_drain_valid=0
_parsed_drain_canonical="90s"
_parsed_drain_sec=90

_parse_drain_timeout_core() {
  local raw="${1:-}"
  local trimmed canonical rest num_part unit int_part frac_part
  local unit_ns mult pow10 max_int_digits int_ns frac_ns
  local frac_padded frac_head sub_digits drain_sec
  local total_ns=0
  local max_ns=3600000000000
  local saved_lc_all="${LC_ALL-}"
  local had_lc_all=0

  _parsed_drain_valid=0
  _parsed_drain_canonical="90s"
  _parsed_drain_sec=90

  if [ "${LC_ALL+x}" = "x" ]; then
    had_lc_all=1
  fi

  trimmed="${raw#"${raw%%[![:space:]]*}"}"
  trimmed="${trimmed%"${trimmed##*[![:space:]]}"}"
  if [ -z "${trimmed}" ]; then
    _parsed_drain_valid=1
    _parsed_drain_canonical="90s"
    _parsed_drain_sec=90
    return 0
  fi

  if [ "${#trimmed}" -gt 32 ]; then
    return 0
  fi

  if [[ "${trimmed}" =~ ^[0123456789]+$ ]]; then
    int_part="${trimmed#"${trimmed%%[!0]*}"}"
    if [ -z "${int_part}" ] || [ "${#int_part}" -gt 4 ] ||
      [ "$((10#${int_part}))" -lt 1 ] || [ "$((10#${int_part}))" -gt 3600 ]; then
      return 0
    fi
    _parsed_drain_valid=1
    _parsed_drain_sec="$((10#${int_part}))"
    _parsed_drain_canonical="${_parsed_drain_sec}s"
    return 0
  fi

  canonical="${trimmed}"
  if [ "${canonical:0:1}" = "+" ]; then
    canonical="${canonical:1}"
  fi
  canonical="${canonical//$'\xc2\xb5'/u}"
  canonical="${canonical//$'\xce\xbc'/u}"

  if [ -z "${canonical}" ] || [ "${#canonical}" -gt 32 ]; then
    return 0
  fi

  LC_ALL=C
  rest="${canonical}"
  while [ -n "${rest}" ]; do
    if [[ "${rest}" =~ ^([0123456789]+(\.[0123456789]*)?|\.[0123456789]+)(ns|us|ms|h|m|s)(.*)$ ]]; then
      num_part="${BASH_REMATCH[1]}"
      unit="${BASH_REMATCH[3]}"
      rest="${BASH_REMATCH[4]}"
    else
      total_ns=-1
      break
    fi

    case "${unit}" in
      ns) unit_ns=1;             mult=1;  pow10=0;  max_int_digits=13 ;;
      us) unit_ns=1000;          mult=1;  pow10=3;  max_int_digits=10 ;;
      ms) unit_ns=1000000;       mult=1;  pow10=6;  max_int_digits=7  ;;
      s)  unit_ns=1000000000;    mult=1;  pow10=9;  max_int_digits=4  ;;
      m)  unit_ns=60000000000;   mult=6;  pow10=10; max_int_digits=2  ;;
      h)  unit_ns=3600000000000; mult=36; pow10=11; max_int_digits=1  ;;
      *)  total_ns=-1; break ;;
    esac

    if [[ "${num_part}" == *.* ]]; then
      int_part="${num_part%%.*}"
      frac_part="${num_part#*.}"
    else
      int_part="${num_part}"
      frac_part=""
    fi

    int_part="${int_part#"${int_part%%[!0]*}"}"
    int_ns=0
    if [ -n "${int_part}" ]; then
      if [ "${#int_part}" -gt "${max_int_digits}" ]; then
        total_ns=-1
        break
      fi
      int_ns=$((10#${int_part} * unit_ns))
    fi

    frac_ns=0
    if [ -n "${frac_part}" ] && [ "${pow10}" -gt 0 ]; then
      frac_padded="${frac_part}00000000000000"
      frac_head="${frac_padded:0:${pow10}}"
      frac_ns=$((10#${frac_head} * mult))
      sub_digits="${frac_padded:${pow10}:3}"
      frac_ns=$((frac_ns + (10#${sub_digits} * mult) / 1000))
    fi

    total_ns=$((total_ns + int_ns + frac_ns))
    if [ "${total_ns}" -gt "${max_ns}" ]; then
      total_ns=-1
      break
    fi
  done

  if [ "${had_lc_all}" -eq 1 ]; then
    export LC_ALL="${saved_lc_all}"
  else
    unset LC_ALL
  fi

  if [ "${total_ns}" -le 0 ] || [ "${total_ns}" -gt "${max_ns}" ]; then
    return 0
  fi

  drain_sec=$(((total_ns + 999999999) / 1000000000))
  if [ "${drain_sec}" -lt 1 ] || [ "${drain_sec}" -gt 3600 ]; then
    return 0
  fi

  _parsed_drain_valid=1
  _parsed_drain_canonical="${canonical}"
  _parsed_drain_sec="${drain_sec}"
}

resolve_drain_timeout_sec() {
  local raw="${1:-}"
  local trimmed="${raw#"${raw%%[![:space:]]*}"}"
  trimmed="${trimmed%"${trimmed##*[![:space:]]}"}"
  _parse_drain_timeout_core "${raw}"
  if [ "${_parsed_drain_valid}" -ne 1 ]; then
    echo "Invalid CPAMP_DRAIN_TIMEOUT (${trimmed}); falling back to 90s" >&2
    printf '90\n'
    return 0
  fi
  printf '%s\n' "${_parsed_drain_sec}"
}


_parse_env_file_kv() {
  local env_file="$1"
  local callback_fn="$2"
  local raw_line tab_scrubbed trimmed key val parsed_val ch rest i len escaped closed tail

  while IFS= read -r raw_line || [ -n "${raw_line}" ]; do
    raw_line="${raw_line%$'\r'}"
    tab_scrubbed="${raw_line//$'\t'/ }"
    case "${tab_scrubbed}" in
      *[[:cntrl:]]*)
        echo "Refusing control character in EnvironmentFile: ${env_file}" >&2
        exit 1
        ;;
    esac

    trimmed="${raw_line#"${raw_line%%[![:space:]]*}"}"
    trimmed="${trimmed%"${trimmed##*[![:space:]]}"}"
    case "${trimmed}" in
      '' | '#'* | ';'*) continue ;;
    esac

    if [[ "${trimmed}" =~ ^export[[:space:]]+(.*)$ ]]; then
      trimmed="${BASH_REMATCH[1]}"
      trimmed="${trimmed#"${trimmed%%[![:space:]]*}"}"
    fi

    if ! [[ "${trimmed}" =~ ^([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]]; then
      continue
    fi
    key="${BASH_REMATCH[1]}"
    val="${BASH_REMATCH[2]}"
    val="${val#"${val%%[![:space:]]*}"}"

    parsed_val=""
    if [[ "${val}" == \"* ]]; then
      rest="${val:1}"
      len="${#rest}"
      i=0
      escaped=0
      closed=0
      while [ "${i}" -lt "${len}" ]; do
        ch="${rest:${i}:1}"
        if [ "${escaped}" -eq 1 ]; then
          if [ "${ch}" = '"' ] || [ "${ch}" = '\' ] || [ "${ch}" = '$' ]; then
            parsed_val="${parsed_val}${ch}"
          else
            parsed_val="${parsed_val}\\${ch}"
          fi
          escaped=0
          i=$((i + 1))
          continue
        fi
        if [ "${ch}" = '\' ]; then
          escaped=1
          i=$((i + 1))
          continue
        fi
        if [ "${ch}" = '"' ]; then
          closed=1
          tail="${rest:$((i + 1))}"
          tail="${tail#"${tail%%[![:space:]]*}"}"
          if [ -n "${tail}" ] && [[ "${tail}" != \#* ]] && [[ "${tail}" != \;* ]]; then
            echo "Refusing malformed quoted value in EnvironmentFile: ${env_file} (key ${key})" >&2
            exit 1
          fi
          break
        fi
        parsed_val="${parsed_val}${ch}"
        i=$((i + 1))
      done
      if [ "${closed}" -ne 1 ]; then
        echo "Refusing unterminated double-quoted value in EnvironmentFile: ${env_file} (key ${key})" >&2
        exit 1
      fi
    elif [[ "${val}" == \'* ]]; then
      rest="${val:1}"
      if [[ "${rest}" != *\'* ]]; then
        echo "Refusing unterminated single-quoted value in EnvironmentFile: ${env_file} (key ${key})" >&2
        exit 1
      fi
      parsed_val="${rest%%\'*}"
      tail="${rest#*\'}"
      tail="${tail#"${tail%%[![:space:]]*}"}"
      if [ -n "${tail}" ] && [[ "${tail}" != \#* ]] && [[ "${tail}" != \;* ]]; then
        echo "Refusing malformed single-quoted value in EnvironmentFile: ${env_file} (key ${key})" >&2
        exit 1
      fi
    else
      if [[ "${val}" == \#* ]]; then
        val=""
      else
        val="${val%%[[:space:]]#*}"
      fi
      val="${val%"${val##*[![:space:]]}"}"
      parsed_val="${val}"
    fi

    "${callback_fn}" "${key}" "${parsed_val}"
  done <"${env_file}"
}

_stop_env_drain_raw=""
_capture_stop_env_drain() {
  if [ "$1" = "CPAMP_DRAIN_TIMEOUT" ]; then
    _stop_env_drain_raw="$2"
  fi
}

resolve_active_instance_drain_sec() {
  local raw_val="" trimmed_val="" int_part="" drain_line=""

  if [ ! -L "${run_dir}/launcher.drain" ] && [ -f "${run_dir}/launcher.drain" ]; then
    raw_val="$(sed -n '1p' "${run_dir}/launcher.drain" 2>/dev/null | tr -d '\r' || true)"
    trimmed_val="${raw_val#"${raw_val%%[![:space:]]*}"}"
    trimmed_val="${trimmed_val%"${trimmed_val##*[![:space:]]}"}"
    if [[ "${trimmed_val}" =~ ^[0123456789]+$ ]]; then
      int_part="${trimmed_val#"${trimmed_val%%[!0]*}"}"
      if [ -n "${int_part}" ] && [ "${#int_part}" -le 4 ] &&
        [ "$((10#${int_part}))" -ge 1 ] && [ "$((10#${int_part}))" -le 3600 ]; then
        printf '%s\n' "$((10#${int_part}))"
        return 0
      fi
    fi
  fi

  if [ ! -L "${run_dir}/launcher.ready" ] && [ -f "${run_dir}/launcher.ready" ]; then
    while IFS= read -r drain_line || [ -n "${drain_line}" ]; do
      drain_line="${drain_line%$'\r'}"
      case "${drain_line}" in
        drain_timeout_sec=*)
          raw_val="${drain_line#drain_timeout_sec=}"
          trimmed_val="${raw_val#"${raw_val%%[![:space:]]*}"}"
          trimmed_val="${trimmed_val%"${trimmed_val##*[![:space:]]}"}"
          if [[ "${trimmed_val}" =~ ^[0123456789]+$ ]]; then
            int_part="${trimmed_val#"${trimmed_val%%[!0]*}"}"
            if [ -n "${int_part}" ] && [ "${#int_part}" -le 4 ] &&
              [ "$((10#${int_part}))" -ge 1 ] && [ "$((10#${int_part}))" -le 3600 ]; then
              printf '%s\n' "$((10#${int_part}))"
              return 0
            fi
          fi
          ;;
      esac
    done <"${run_dir}/launcher.ready"
  fi

  if [ -n "${CPAMP_DRAIN_TIMEOUT:-}" ]; then
    resolve_drain_timeout_sec "${CPAMP_DRAIN_TIMEOUT}"
    return 0
  fi

  if [ ! -L "${home_dir}/cpa-manager-plus.env" ] && [ -f "${home_dir}/cpa-manager-plus.env" ]; then
    _stop_env_drain_raw=""
    _parse_env_file_kv "${home_dir}/cpa-manager-plus.env" "_capture_stop_env_drain"
    if [ -n "${_stop_env_drain_raw}" ]; then
      resolve_drain_timeout_sec "${_stop_env_drain_raw}"
      return 0
    fi
  fi

  printf '90\n'
}
stop_app() {
  set_pid_record_state
  case "${record_state}" in
    missing)
      if is_v2_mode; then
        cleanup_v2_children
      fi
      echo "${app_name} is not running"
      return 0
      ;;
    stale | invalid)
      rm -f "${pid_file}"
      if is_v2_mode; then
        cleanup_v2_children
      fi
      echo "Removed stale PID file for ${app_name}"
      return 0
      ;;
    conflict)
      echo "Refusing to stop: ${pid_file} points to a running process that could not be strongly verified." >&2
      exit 1
      ;;
  esac

  local pid="${record_pid}"
  local drain_sec=90
  if is_v2_mode; then
    drain_sec="$(resolve_active_instance_drain_sec)"
  fi

  kill "${pid}"
  if is_v2_mode; then
    local stop_timeout min_stop_timeout raw_stop
    min_stop_timeout=$((drain_sec + 45))
    if [ -n "${CPAMP_STOP_TIMEOUT_SEC:-}" ]; then
      if [[ "${CPAMP_STOP_TIMEOUT_SEC}" =~ ^[0123456789]+$ ]] &&
        [ "${#CPAMP_STOP_TIMEOUT_SEC}" -le 5 ] &&
        [ "$((10#${CPAMP_STOP_TIMEOUT_SEC}))" -ge 1 ]; then
        raw_stop="$((10#${CPAMP_STOP_TIMEOUT_SEC}))"
        stop_timeout="${raw_stop}"
        if [ "${stop_timeout}" -lt "${min_stop_timeout}" ]; then
          echo "Clamping CPAMP_STOP_TIMEOUT_SEC (${raw_stop}s -> ${min_stop_timeout}s) to honor active ${drain_sec}s Ingress drain budget" >&2
          stop_timeout="${min_stop_timeout}"
        fi
      else
        echo "Invalid CPAMP_STOP_TIMEOUT_SEC (${CPAMP_STOP_TIMEOUT_SEC}); computing bounded default" >&2
        stop_timeout=$((drain_sec + 45))
      fi
    else
      stop_timeout=$((drain_sec + 45))
    fi
    local max_ticks=$((stop_timeout * 10))
    local tick=0
    while [ "${tick}" -lt "${max_ticks}" ]; do
      if ! recorded_instance_running &&
        ! child_pid_if_active "${run_dir}/ingress.pid" >/dev/null 2>&1 &&
        ! child_pid_if_active "${run_dir}/manager.pid" >/dev/null 2>&1 &&
        ! child_pid_if_active "${run_dir}/runtime-supervisor.pid" >/dev/null 2>&1; then
        reap_orphaned_cpa_processes
        rm -f "${pid_file}" "${run_dir}/launcher.ready" "${run_dir}/launcher.drain" \
          "${run_dir}/ingress.pid" "${run_dir}/manager.pid" "${run_dir}/runtime-supervisor.pid"
        echo "${app_name} stopped"
        return 0
      fi
      sleep 0.1
      tick=$((tick + 1))
    done
    cleanup_v2_children
    if ! recorded_instance_running; then
      rm -f "${pid_file}"
      echo "${app_name} stopped"
      return 0
    fi
    echo "${app_name} did not stop within ${stop_timeout} seconds. PID: ${pid}" >&2
    exit 1
  fi

  for _ in 1 2 3 4 5 6 7 8 9 10; do
    if ! recorded_instance_running; then
      rm -f "${pid_file}"
      echo "${app_name} stopped"
      return 0
    fi
    sleep 1
  done

  echo "${app_name} did not stop within 10 seconds. PID: ${pid}" >&2
  exit 1
}

status_app() {
  set_pid_record_state
  case "${record_state}" in
    active)
      echo "${app_name} is running with PID ${record_pid}"
      if is_v2_mode; then
        local degraded=0 c_name c_pid
        for c_name in runtime-supervisor manager ingress; do
          if c_pid="$(child_pid_if_active "${run_dir}/${c_name}.pid" 2>/dev/null)"; then
            echo "${c_name}: running (PID ${c_pid})"
          else
            echo "${c_name}: stopped"
            degraded=1
          fi
        done
        echo "PID file: ${pid_file}"
        echo "Log: ${log_file}"
        if [ "${degraded}" -ne 0 ]; then
          echo "${app_name} is degraded: one or more v2 child processes are not running" >&2
          return 1
        fi
        return 0
      fi
      echo "PID file: ${pid_file}"
      echo "Log: ${log_file}"
      return 0
      ;;
    missing)
      echo "${app_name} is not running"
      return 1
      ;;
    stale | invalid)
      echo "${app_name} is not running; stale PID file: ${pid_file}"
      return 1
      ;;
    conflict)
      echo "${app_name} status is unknown; ${pid_file} points to a running process that could not be strongly verified."
      return 1
      ;;
  esac
}

show_logs() {
  if [ "$#" -gt 1 ]; then
    echo "Usage: $(basename "$0") logs [lines|-f|--follow]" >&2
    exit 1
  fi

  local option="${1:-80}"
  if [ "${option}" != "-f" ] && [ "${option}" != "--follow" ] && ! [[ "${option}" =~ ^[1-9][0-9]*$ ]]; then
    echo "Invalid log line count: ${option}" >&2
    exit 1
  fi

  if [ ! -f "${log_file}" ]; then
    echo "Log file does not exist yet: ${log_file}" >&2
    exit 1
  fi

  if [ "${option}" = "-f" ] || [ "${option}" = "--follow" ]; then
    tail -n 80 -f "${log_file}"
    return 0
  fi

  tail -n "${option}" "${log_file}"
}

split_host_port() {
  local addr="$1"
  local host port

  if [[ "${addr}" =~ ^\[([^]]+)\]:([0-9]+)$ ]]; then
    host="[${BASH_REMATCH[1]}]"
    port="${BASH_REMATCH[2]}"
  elif [[ "${addr}" =~ ^([^:]*):([0-9]+)$ ]]; then
    host="${BASH_REMATCH[1]}"
    port="${BASH_REMATCH[2]}"
  else
    return 1
  fi
  # A port reaches $((...)) and printf '%04X' as a numeric operand; a
  # non-canonical string such as "08317" would be read as octal (or rejected by
  # the shell) instead of the decimal the operator meant, so only canonical
  # decimal spellings are accepted.
  case "${port}" in
    '' | 0*) return 1 ;;
  esac
  if [ "${#port}" -gt 5 ]; then
    return 1
  fi
  if [ "$((10#${port}))" -lt 1 ] || [ "$((10#${port}))" -gt 65535 ]; then
    return 1
  fi
  printf '%s\n%s\n' "${host}" "${port}"
}

read_persisted_cpa_address() {
  local source_run_dir="${1:-${run_dir}}"
  local persisted="${source_run_dir}/cpa.address"
  local candidate=""

  if [ -f "${persisted}" ] && [ ! -L "${persisted}" ]; then
    candidate="$(sed -n '1p' "${persisted}" 2>/dev/null | tr -d '[:space:]')"
    if [ -n "${candidate}" ] && split_host_port "${candidate}" >/dev/null 2>&1; then
      printf '%s\n' "${candidate}"
      return 0
    fi
  fi
  return 1
}

cpa_address_from_environment() {
  local candidate

  if [ -n "${CPAMP_RUNTIME_CPA_ADDR:-}" ]; then
    candidate="${CPAMP_RUNTIME_CPA_ADDR}"
  elif [ -n "${CPAMP_CPA_PORT:-}" ]; then
    candidate="127.0.0.1:${CPAMP_CPA_PORT}"
  elif [ -n "${CPAMP_GATEWAY_URL:-}" ]; then
    candidate="$(extract_host_port_from_url "${CPAMP_GATEWAY_URL}")"
  elif [ -n "${CPA_UPSTREAM_URL:-}" ]; then
    candidate="$(extract_host_port_from_url "${CPA_UPSTREAM_URL}")"
  else
    candidate="127.0.0.1:8317"
  fi
  printf '%s\n' "${candidate}"
}

# One resolver for the CPA loopback address so `ctl` and the launcher can never
# disagree. The launcher records the address it actually used in
# ${run_dir}/cpa.address; that record wins because a stop must target the running
# instance, not whatever this shell happens to export.
resolve_cpa_address() {
  read_persisted_cpa_address || cpa_address_from_environment
}

# `start` and `install` are acting on a specific instance, so they resolve in
# this order: explicit override, that instance's own recorded address, then the
# documented default. `start` passes this installation's run directory and
# `install` passes the target's, so installing instance B never inherits
# instance A's port and a restart can never drift back to the hardcoded 8317.
resolve_cpa_address_for_instance() {
  local instance_run_dir="$1"

  if [ -n "${CPAMP_RUNTIME_CPA_ADDR:-}" ] || [ -n "${CPAMP_CPA_PORT:-}" ] ||
    [ -n "${CPAMP_GATEWAY_URL:-}" ] || [ -n "${CPA_UPSTREAM_URL:-}" ]; then
    cpa_address_from_environment
    return 0
  fi
  read_persisted_cpa_address "${instance_run_dir}" || printf '%s\n' "127.0.0.1:8317"
}

extract_host_port_from_url() {
  local raw="$1"
  local stripped="${raw#http://}"
  stripped="${stripped#https://}"
  stripped="${stripped%%/*}"
  printf '%s\n' "${stripped}"
}

require_safe_unit_value() {
  local label="$1"
  local value="$2"

  # systemd expands `$` in ExecStart= and Environment= values, treats `"` as a
  # quote delimiter, interprets `%` as a specifier, and swallows `\` as an
  # escape character - a value carrying any of them would not be written
  # verbatim into the unit. Control characters are refused outright; printable
  # non-ASCII stays allowed so a UTF-8 install path still works.
  case "${value}" in
    '')
      echo "Refusing to write an empty ${label} into the systemd unit" >&2
      exit 1
      ;;
    *[[:space:]]* | *[[:cntrl:]]* | *'"'* | *'%'* | *'\'* | *'$'*)
      echo "Refusing to write ${label} into the systemd unit: ${value}" >&2
      exit 1
      ;;
  esac
}

require_safe_account_name() {
  local label="$1"
  local value="$2"

  if ! [[ "${value}" =~ ^[a-z_][a-z0-9_-]*$ ]]; then
    echo "Invalid ${label}: ${value} (must match [a-z_][a-z0-9_-]*)" >&2
    exit 1
  fi
}

require_absolute_install_path() {
  local label="$1"
  local value="$2"

  case "${value}" in
    /*) ;;
    *)
      echo "${label} must be an absolute path: ${value}" >&2
      exit 1
      ;;
  esac
  require_safe_unit_value "${label}" "${value}"
}

require_install_home() {
  local value="$1"

  require_absolute_install_path "install home" "${value}"
  case "${value}" in
    /home/* | /root | /root/* | /run/user/*)
      echo "Refusing to install under ${value}: ProtectHome masks /home, /root and /run/user" >&2
      exit 1
      ;;
  esac
}

require_install_service_name() {
  local name="$1"

  if ! [[ "${name}" =~ ^[A-Za-z0-9][A-Za-z0-9_.@-]*\.service$ ]]; then
    echo "Invalid systemd service name: ${name}" >&2
    exit 1
  fi
}

require_unit_address() {
  local label="$1"
  local addr="$2"
  local loopback_only="$3"
  local parsed host port

  parsed="$(split_host_port "${addr}")" || {
    echo "Invalid ${label} address: ${addr}" >&2
    exit 1
  }
  host="$(printf '%s\n' "${parsed}" | sed -n '1p')"
  port="$(printf '%s\n' "${parsed}" | sed -n '2p')"
  if [ "${loopback_only}" = "true" ]; then
    case "${host}" in
      127.0.0.1 | localhost | "[::1]") ;;
      *)
        echo "Refusing non-loopback internal address for ${label}: ${addr}" >&2
        exit 1
        ;;
    esac
  fi
  require_safe_unit_value "${label} address" "${addr}"
}

resolve_cpa_identity() {
  local cpa_user="${CPAMP_CPA_USER:-cpamp-cpa}"
  local uid="${CPAMP_RUNTIME_CPA_UID:-}"
  local gid="${CPAMP_RUNTIME_CPA_GID:-}"
  local target_home="$1"

  require_safe_account_name "CPAMP_CPA_USER" "${cpa_user}"

  # A half-configured identity used to be silently completed from the account
  # database, which could hand CPA a UID/GID pair the operator never chose.
  if { [ -n "${uid}" ] && [ -z "${gid}" ]; } || { [ -z "${uid}" ] && [ -n "${gid}" ]; }; then
    echo "CPAMP_RUNTIME_CPA_UID and CPAMP_RUNTIME_CPA_GID must be configured together" >&2
    exit 1
  fi

  if [ -z "${uid}" ] && id -u "${cpa_user}" >/dev/null 2>&1; then
    uid="$(id -u "${cpa_user}")"
    gid="$(id -g "${cpa_user}")"
  fi

  if [ -z "${uid}" ] && [ "$(id -u)" -eq 0 ] && command -v useradd >/dev/null 2>&1; then
    if ! id -u "${cpa_user}" >/dev/null 2>&1; then
      useradd --system --home-dir "${target_home}" --no-create-home \
        --shell /usr/sbin/nologin "${cpa_user}" >/dev/null 2>&1 || true
    fi
    if id -u "${cpa_user}" >/dev/null 2>&1; then
      uid="$(id -u "${cpa_user}")"
      gid="$(id -g "${cpa_user}")"
    fi
  fi

  if [ -z "${uid}" ] || [ -z "${gid}" ]; then
    uid="10001"
    gid="10001"
    if [ "$(id -u)" -eq 0 ]; then
      echo "Warning: could not create ${cpa_user}; using UID/GID ${uid}:${gid}" >&2
    fi
  fi

  case "${uid}" in
    '' | *[!0-9]*)
      echo "CPAMP_RUNTIME_CPA_UID must be a positive decimal integer" >&2
      exit 1
      ;;
  esac
  case "${gid}" in
    '' | *[!0-9]*)
      echo "CPAMP_RUNTIME_CPA_GID must be a positive decimal integer" >&2
      exit 1
      ;;
  esac
  if [ "${uid}" -eq 0 ] || [ "${gid}" -eq 0 ]; then
    echo "The CPA child identity must not be root (UID/GID must be non-zero)" >&2
    exit 1
  fi

  cpa_uid="${uid}"
  cpa_gid="${gid}"
  cpa_user_name="${cpa_user}"
}

# Docker's ownership layout, reproduced for the native install: private
# Supervisor state and the token stay root-owned, Supervisor directories are
# root:<cpa gid> 0710 so the CPA child can traverse but not read them, and the
# Gateway tree belongs to the CPA identity.
#
# NH1: runtime_root lives inside data_dir, and the dropped CPA child needs a
# traverse bit on every directory above its config, auth dir and executable.
# data_dir therefore becomes `root:<cpa gid>` 0710 (traverse only) while every
# Manager-owned entry inside it is forced back to `0:0` with `0600`/`0700`.
apply_install_ownership() {
  local target_data_dir="$1"
  local target_run_dir="$2"
  local target_log_dir="$3"
  local target_token_file="$4"
  local runtime_root="${target_data_dir}/runtime"
  local supervisor_dir="${runtime_root}/supervisor"
  local gateway_dir="${runtime_root}/gateway"
  local path manager_data_path

  # The Gateway tree belongs to the CPA identity, so refuse a planted link
  # before anything here (root or not) walks it.
  reject_symlinks_under "${gateway_dir}"

  if [ -z "${cpa_uid:-}" ] || [ "$(id -u)" -ne 0 ]; then
    return 0
  fi

  for path in \
    "${target_run_dir}" \
    "${target_log_dir}" \
    "${target_token_file}" \
    "${runtime_root}" \
    "${supervisor_dir}" \
    "${supervisor_dir}/active" \
    "${supervisor_dir}/active/cpa"; do
    [ -e "${path}" ] || continue
    if ! chown -h 0:0 "${path}"; then
      echo "Failed to keep ${path} owned by root" >&2
      exit 1
    fi
  done

  for manager_data_path in "${target_data_dir}"/*; do
    [ -e "${manager_data_path}" ] || continue
    [ "${manager_data_path}" = "${runtime_root}" ] && continue
    if [ -L "${manager_data_path}" ]; then
      echo "Refusing to continue: ${manager_data_path} is a symlink inside the Manager data directory" >&2
      exit 1
    fi
    if [ -d "${manager_data_path}" ]; then
      # Recursively drop every group/other bit so nothing Manager-owned inside
      # the (now group-traversable) data directory is reachable by cpamp-cpa.
      if ! chown -R -h 0:0 "${manager_data_path}" ||
        ! chmod 0700 "${manager_data_path}" ||
        ! chmod -R go-rwx "${manager_data_path}"; then
        echo "Failed to keep ${manager_data_path} root-only" >&2
        exit 1
      fi
    else
      if ! chown -h 0:0 "${manager_data_path}" || ! chmod 0600 "${manager_data_path}"; then
        echo "Failed to keep ${manager_data_path} root-only" >&2
        exit 1
      fi
    fi
  done

  for path in "${supervisor_dir}" "${supervisor_dir}/artifacts" "${supervisor_dir}/artifacts/cpa"; do
    [ -d "${path}" ] || continue
    if ! chown -h 0:"${cpa_gid}" "${path}" || ! chmod 0710 "${path}"; then
      echo "Failed to hand ${path} to root:${cpa_gid} with mode 0710" >&2
      exit 1
    fi
  done

  for path in "${target_run_dir}" "${target_log_dir}" "${supervisor_dir}/active" "${supervisor_dir}/active/cpa"; do
    [ -d "${path}" ] || continue
    chmod 0700 "${path}"
  done
  if [ -d "${runtime_root}" ]; then
    chmod 0711 "${runtime_root}"
  fi
  if [ -f "${target_token_file}" ]; then
    chmod 0600 "${target_token_file}"
  fi
  if ! chown -h 0:"${cpa_gid}" "${target_data_dir}" || ! chmod 0710 "${target_data_dir}"; then
    echo "Failed to make ${target_data_dir} traversable for group ${cpa_gid} (mode 0710)" >&2
    exit 1
  fi

  # Mutate the Gateway tree only while root owns it, then hand it to the child.
  reject_symlinks_under "${gateway_dir}"
  if [ -d "${gateway_dir}" ]; then
    if ! chown -h 0:0 "${gateway_dir}" || ! chmod 0700 "${gateway_dir}"; then
      echo "Failed to take temporary ownership of ${gateway_dir}" >&2
      exit 1
    fi
    for path in "${gateway_dir}" "${gateway_dir}/auth" "${gateway_dir}/logs" "${gateway_dir}/plugins"; do
      [ -d "${path}" ] || continue
      if [ -L "${path}" ] || ! chmod 0700 "${path}"; then
        echo "Failed to restrict ${path} to the CPA identity" >&2
        exit 1
      fi
    done
    for path in "${gateway_dir}"/*; do
      [ -f "${path}" ] && [ ! -L "${path}" ] || continue
      chmod 0600 "${path}"
    done
    if ! chown -R -h "${cpa_uid}:${cpa_gid}" "${gateway_dir}"; then
      echo "Failed to hand the Gateway directory ${gateway_dir} to ${cpa_uid}:${cpa_gid}" >&2
      exit 1
    fi
  fi
}

install_service() {
  local unit_dir="${CPA_MANAGER_PLUS_SYSTEMD_DIR:-/etc/systemd/system}"
  local service_name="${CPA_MANAGER_PLUS_SERVICE_NAME:-cpa-manager-plus.service}"
  local skip_systemctl="${CPA_MANAGER_PLUS_SKIP_SYSTEMCTL:-0}"
  local target_home="${home_dir}"

  while [ "$#" -gt 0 ]; do
    case "$1" in
      --unit-dir)
        [ "$#" -ge 2 ] || { echo "Missing value for --unit-dir" >&2; exit 1; }
        unit_dir="$2"
        shift 2
        ;;
      --service-name)
        [ "$#" -ge 2 ] || { echo "Missing value for --service-name" >&2; exit 1; }
        service_name="$2"
        shift 2
        ;;
      --home)
        [ "$#" -ge 2 ] || { echo "Missing value for --home" >&2; exit 1; }
        target_home="$(resolve_path "$2")"
        shift 2
        ;;
      --user | --group)
        echo "install ${1} is not supported: the launcher must run as root so the Runtime Supervisor can drop CPA to its dedicated identity" >&2
        exit 1
        ;;
      --no-systemctl)
        skip_systemctl="1"
        shift
        ;;
      *)
        echo "Unknown install option: $1" >&2
        exit 1
        ;;
    esac
  done

  case "${service_name}" in
    *.service) ;;
    *) service_name="${service_name}.service" ;;
  esac
  require_install_service_name "${service_name}"
  require_absolute_install_path "unit directory" "${unit_dir}"
  require_install_home "${target_home}"
  ensure_no_active_upgrade_lock "${target_home}" "install"

  local target_bin_dir="${CPA_MANAGER_PLUS_BIN_DIR:-"${target_home}/bin"}"
  local target_runtime_bin="${CPA_MANAGER_PLUS_RUNTIME_SUPERVISOR_BIN:-"${target_bin_dir}/cpa-manager-plus-runtime-supervisor"}"
  local target_manager_bin="${CPA_MANAGER_PLUS_MANAGER_BIN:-"${CPA_MANAGER_PLUS_BIN:-"${target_bin_dir}/cpa-manager-plus"}"}"
  local target_ingress_bin="${CPA_MANAGER_PLUS_INGRESS_BIN:-"${target_bin_dir}/cpa-manager-plus-ingress"}"
  local target_cpa_bin="${CPAMP_CPA_EXECUTABLE:-"${target_bin_dir}/cli-proxy-api"}"
  local target_launcher_bin="${CPA_MANAGER_PLUS_LAUNCHER:-"${target_bin_dir}/cpa-manager-plus-launcher"}"

  if [ -n "${CPA_MANAGER_PLUS_LAUNCHER:-}" ]; then
    if [ -L "${target_launcher_bin}" ] || [ ! -f "${target_launcher_bin}" ] || [ ! -x "${target_launcher_bin}" ]; then
      echo "Configured launcher is missing, symlinked, or not executable: ${target_launcher_bin}" >&2
      exit 1
    fi
  elif [ ! -x "${target_launcher_bin}" ]; then
    if ! target_launcher_bin="$(resolve_launcher_bin)"; then
      echo "Missing executable launcher: ${target_bin_dir}/cpa-manager-plus-launcher" >&2
      exit 1
    fi
  fi

  local pair comp_name comp_bin
  for pair in \
    "runtime-supervisor:${target_runtime_bin}" \
    "manager:${target_manager_bin}" \
    "ingress:${target_ingress_bin}" \
    "cli-proxy-api:${target_cpa_bin}"; do
    comp_name="${pair%%:*}"
    comp_bin="${pair#*:}"
    if [ -L "${comp_bin}" ] || [ ! -f "${comp_bin}" ] || [ ! -x "${comp_bin}" ]; then
      echo "Missing or non-executable ${comp_name} binary for install: ${comp_bin}" >&2
      exit 1
    fi
  done

  local raw_drain="${CPAMP_DRAIN_TIMEOUT:-90s}"
  if [ -n "${CPAMP_DRAIN_TIMEOUT+x}" ] && [ -n "${CPAMP_DRAIN_TIMEOUT}" ]; then
    local drain_trimmed_check="${CPAMP_DRAIN_TIMEOUT#"${CPAMP_DRAIN_TIMEOUT%%[![:space:]]*}"}"
    if [ -z "${drain_trimmed_check}" ]; then
      echo "Invalid CPAMP_DRAIN_TIMEOUT: whitespace-only duration" >&2
      exit 1
    fi
  fi
  _parse_drain_timeout_core "${raw_drain}"
  if [ "${_parsed_drain_valid}" -ne 1 ]; then
    echo "Invalid CPAMP_DRAIN_TIMEOUT: ${raw_drain} (must be a positive duration between 1ns and 3600s)" >&2
    exit 1
  fi
  local validated_drain="${_parsed_drain_canonical}"
  local drain_sec="${_parsed_drain_sec}"
  require_safe_unit_value "drain timeout" "${validated_drain}"
  local unit_stop_sec=$((drain_sec + 60))
  if [ "${unit_stop_sec}" -lt 150 ]; then
    unit_stop_sec=150
  fi

  local target_data_dir="${USAGE_DATA_DIR:-"${CPA_MANAGER_PLUS_DATA_DIR:-"${target_home}/data"}"}"
  local target_run_dir="${CPA_MANAGER_PLUS_RUN_DIR:-"${target_home}/run"}"
  local target_log_dir="${CPA_MANAGER_PLUS_LOG_DIR:-"${target_home}/logs"}"
  local target_token_file="${CPAMP_RUNTIME_TOKEN_FILE:-"${target_run_dir}/runtime-token"}"
  local target_db_path="${USAGE_DB_PATH:-"${target_data_dir}/usage.sqlite"}"
  local target_data_key_path="${CPA_MANAGER_DATA_KEY_PATH:-"${target_data_dir}/data.key"}"

  local target_manage_data="${manage_data_dir}"
  local target_manage_run="${manage_run_dir}"
  local target_manage_log="${manage_log_dir}"
  if [ "${target_data_dir}" = "${target_home}/data" ]; then target_manage_data="true"; fi
  if [ "${target_run_dir}" = "${target_home}/run" ]; then target_manage_run="true"; fi
  if [ "${target_log_dir}" = "${target_home}/logs" ]; then target_manage_log="true"; fi

  ensure_private_dir "${target_data_dir}" "${target_manage_data}"
  ensure_private_dir "${target_run_dir}" "${target_manage_run}"
  ensure_private_dir "${target_log_dir}" "${target_manage_log}"

  local runtime_root="${target_data_dir}/runtime"
  local supervisor_dir="${runtime_root}/supervisor"
  local gateway_dir="${runtime_root}/gateway"
  local priv_dir
  for priv_dir in \
    "${runtime_root}" \
    "${supervisor_dir}" \
    "${supervisor_dir}/artifacts" \
    "${supervisor_dir}/artifacts/cpa" \
    "${supervisor_dir}/active" \
    "${supervisor_dir}/active/cpa" \
    "${gateway_dir}" \
    "${gateway_dir}/auth" \
    "${gateway_dir}/logs" \
    "${gateway_dir}/plugins"; do
    ensure_private_dir "${priv_dir}" "true"
  done

  reject_unsafe_runtime_file "${target_token_file}"
  reject_unsafe_runtime_file_parent "${target_token_file}"
  if [ ! -f "${target_token_file}" ]; then
    local token_tmp="${target_token_file}.tmp.$$"
    prepare_private_file "${token_tmp}"
    dd if=/dev/urandom bs=32 count=1 2>/dev/null | od -An -tx1 | tr -d ' \n' >"${token_tmp}"
    chmod 0600 "${token_tmp}"
    mv -f "${token_tmp}" "${target_token_file}"
  fi
  chmod 0600 "${target_token_file}"

  cpa_uid=""
  cpa_gid=""
  cpa_user_name=""
  resolve_cpa_identity "${target_home}"
  apply_install_ownership "${target_data_dir}" "${target_run_dir}" "${target_log_dir}" "${target_token_file}"

  if [ -L "${unit_dir}" ]; then
    echo "Refusing symlinked unit directory: ${unit_dir}" >&2
    exit 1
  fi
  mkdir -p "${unit_dir}"
  local unit_path="${unit_dir}/${service_name}"
  if [ -L "${unit_path}" ]; then
    echo "Refusing symlinked systemd unit path: ${unit_path}" >&2
    exit 1
  fi
  if [ -e "${unit_path}" ] && [ ! -f "${unit_path}" ]; then
    echo "Refusing non-regular systemd unit path: ${unit_path}" >&2
    exit 1
  fi

  local public_http_addr="${HTTP_ADDR:-"${CPAMP_INGRESS_ADDR:-0.0.0.0:18317}"}"
  local runtime_addr="${CPAMP_RUNTIME_ADDR:-127.0.0.1:18318}"
  local manager_addr="${CPAMP_MANAGER_ADDR:-127.0.0.1:18319}"
  local ingress_control_addr="${CPAMP_INGRESS_CONTROL_ADDR:-127.0.0.1:18320}"
  local cpa_addr
  cpa_addr="$(resolve_cpa_address_for_instance "${target_run_dir}")"

  require_unit_address "public ingress" "${public_http_addr}" "false"
  require_unit_address "runtime" "${runtime_addr}" "true"
  require_unit_address "manager" "${manager_addr}" "true"
  require_unit_address "ingress-control" "${ingress_control_addr}" "true"
  require_unit_address "cpa" "${cpa_addr}" "true"
  require_absolute_install_path "data directory" "${target_data_dir}"
  require_absolute_install_path "run directory" "${target_run_dir}"
  require_absolute_install_path "log directory" "${target_log_dir}"
  require_absolute_install_path "database path" "${target_db_path}"
  require_absolute_install_path "data key path" "${target_data_key_path}"
  require_absolute_install_path "runtime token path" "${target_token_file}"
  require_safe_unit_value "launcher path" "${target_launcher_bin}"
  require_safe_unit_value "CPA executable" "${target_cpa_bin}"

  local rw_paths="${target_data_dir} ${target_run_dir} ${target_log_dir}"
  local extra_file extra_dir
  for extra_file in "${target_db_path}" "${target_data_key_path}" "${target_token_file}"; do
    extra_dir="$(dirname "${extra_file}")"
    case "${extra_dir}" in
      "${target_data_dir}" | "${target_data_dir}"/* | \
      "${target_run_dir}" | "${target_run_dir}"/* | \
      "${target_log_dir}" | "${target_log_dir}"/*)
        continue
        ;;
    esac
    case " ${rw_paths} " in
      *" ${extra_dir} "*) continue ;;
    esac
    if [ -d "${extra_dir}" ]; then
      rw_paths="${rw_paths} ${extra_dir}"
    fi
  done
  if [ -n "${CPAMP_PRESERVED_RW_PATHS:-}" ]; then
    local preserved_rw_line preserved_rw preserved_rw_check rw_seen existing_rw
    local restore_noglob=0
    case "$-" in *f*) restore_noglob=1 ;; esac
    set -f
    while IFS= read -r preserved_rw_line || [ -n "${preserved_rw_line}" ]; do
      for preserved_rw in ${preserved_rw_line}; do
        [ -n "${preserved_rw}" ] || continue
        require_safe_unit_value "preserved ReadWritePaths entry" "${preserved_rw}"
        preserved_rw_check="${preserved_rw#-}"
        case "${preserved_rw_check}" in
          /*) ;;
          *)
            [ "${restore_noglob}" -eq 0 ] && set +f
            echo "Refusing non-absolute preserved ReadWritePaths entry: ${preserved_rw}" >&2
            exit 1
            ;;
        esac
        rw_seen=0
        for existing_rw in ${rw_paths}; do
          if [ "${existing_rw}" = "${preserved_rw}" ]; then
            rw_seen=1
            break
          fi
        done
        if [ "${rw_seen}" -eq 0 ]; then
          rw_paths="${rw_paths} ${preserved_rw}"
        fi
      done
    done <<<"${CPAMP_PRESERVED_RW_PATHS}"
    [ "${restore_noglob}" -eq 0 ] && set +f
  fi

  local env_file_lines=""
  if [ -n "${CPAMP_PRESERVED_ENV_FILES:-}" ]; then
    local ef_line ef_check
    while IFS= read -r ef_line || [ -n "${ef_line}" ]; do
      [ -n "${ef_line}" ] || continue
      ef_check="${ef_line#-}"
      require_absolute_install_path "preserved EnvironmentFile path" "${ef_check}"
      env_file_lines="${env_file_lines}EnvironmentFile=${ef_line}
"
    done <<<"${CPAMP_PRESERVED_ENV_FILES}"
  fi
  if [ -z "${env_file_lines}" ]; then
    env_file_lines="EnvironmentFile=-${target_home}/cpa-manager-plus.env
"
  fi

  local unit_tmp="${unit_path}.tmp.$$"
  if [ -L "${unit_tmp}" ]; then
    echo "Refusing symlinked temporary unit path: ${unit_tmp}" >&2
    exit 1
  fi
  {
    cat <<EOF
[Unit]
Description=CPA Manager Plus v2 (3-Process Native Service: Runtime Supervisor, Manager, Ingress)
Documentation=https://github.com/seakee/CPA-Manager-Plus
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=${target_home}
Environment="CPA_MANAGER_PLUS_HOME=${target_home}"
Environment="HTTP_ADDR=${public_http_addr}"
Environment="USAGE_DATA_DIR=${target_data_dir}"
Environment="USAGE_DB_PATH=${target_db_path}"
Environment="CPA_MANAGER_DATA_KEY_PATH=${target_data_key_path}"
Environment="CPA_MANAGER_PLUS_RUN_DIR=${target_run_dir}"
Environment="CPA_MANAGER_PLUS_LOG_DIR=${target_log_dir}"
Environment="CPAMP_RUNTIME_TOKEN_FILE=${target_token_file}"
Environment="CPAMP_CPA_EXECUTABLE=${target_cpa_bin}"
Environment="CPAMP_RUNTIME_ADDR=${runtime_addr}"
Environment="CPAMP_MANAGER_ADDR=${manager_addr}"
Environment="CPAMP_INGRESS_CONTROL_ADDR=${ingress_control_addr}"
Environment="CPAMP_RUNTIME_CPA_ADDR=${cpa_addr}"
Environment="CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK=1"
Environment="CPAMP_DRAIN_TIMEOUT=${validated_drain}"
Environment="CPAMP_RUNTIME_CPA_UID=${cpa_uid}"
Environment="CPAMP_RUNTIME_CPA_GID=${cpa_gid}"
Environment="CPAMP_SERVICE_STOP_TIMEOUT_SEC=${unit_stop_sec}"
$(printf '%s' "${env_file_lines}" | sed '/^$/d')
ExecStart=${target_launcher_bin}
KillMode=mixed
KillSignal=SIGTERM
TimeoutStopSec=${unit_stop_sec}
Restart=on-failure
RestartSec=5
UMask=0077
ProtectSystem=strict
ReadWritePaths=${rw_paths}
ProtectHome=true
PrivateDevices=true
PrivateTmp=true
NoNewPrivileges=true
RestrictSUIDSGID=true
LockPersonality=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
ProtectClock=true
ProtectHostname=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
SystemCallArchitectures=native
CapabilityBoundingSet=CAP_CHOWN CAP_DAC_OVERRIDE CAP_FOWNER CAP_KILL CAP_SETGID CAP_SETUID

[Install]
WantedBy=multi-user.target
EOF
  } >"${unit_tmp}" || {
    rm -f "${unit_tmp}" 2>/dev/null || true
    exit 1
  }
  chmod 0644 "${unit_tmp}" || {
    rm -f "${unit_tmp}" 2>/dev/null || true
    exit 1
  }
  if [ "$(id -u)" -eq 0 ]; then
    chown -h 0:0 "${unit_tmp}" || {
      rm -f "${unit_tmp}" 2>/dev/null || true
      exit 1
    }
  fi
  mv -f "${unit_tmp}" "${unit_path}" || {
    rm -f "${unit_tmp}" 2>/dev/null || true
    exit 1
  }

  local systemctl_bin="${CPA_MANAGER_PLUS_SYSTEMCTL_BIN:-}"
  if [ -z "${systemctl_bin}" ] && command -v systemctl >/dev/null 2>&1 && [ "$(id -u)" -eq 0 ]; then
    systemctl_bin="systemctl"
  fi

  if [ "${skip_systemctl}" != "1" ] && [ -n "${systemctl_bin}" ]; then
    "${systemctl_bin}" daemon-reload
    "${systemctl_bin}" enable "${service_name}"
  fi

  echo "Installed systemd unit: ${unit_path}"
  echo "CPA child identity: ${cpa_user_name} (UID ${cpa_uid}, GID ${cpa_gid})"
}

_cpamp_upgrade_stage_dir=""
_cpamp_upgrade_lock_dir=""
_cpamp_upgrade_lock_token=""
_cpamp_swap_incomplete=0
_cpamp_cleanup_upgrade_dirs() {
  if [ -n "${_cpamp_upgrade_stage_dir:-}" ] && [ -d "${_cpamp_upgrade_stage_dir}" ] && [ ! -L "${_cpamp_upgrade_stage_dir}" ]; then
    rm -rf "${_cpamp_upgrade_stage_dir}" || true
  fi
  _cpamp_upgrade_stage_dir=""
  if [ "${_cpamp_swap_incomplete:-0}" -eq 1 ]; then
    if [ -n "${_cpamp_upgrade_lock_dir:-}" ] && [ -d "${_cpamp_upgrade_lock_dir}" ]; then
      echo "CRITICAL: Incomplete upgrade swap; preserving lock ${_cpamp_upgrade_lock_dir} until manual recovery" >&2
    fi
    _cpamp_upgrade_lock_dir=""
    return 0
  fi
  if [ -n "${_cpamp_upgrade_lock_dir:-}" ] && [ -d "${_cpamp_upgrade_lock_dir}" ] && [ ! -L "${_cpamp_upgrade_lock_dir}" ]; then
    rm -f \
      "${_cpamp_upgrade_lock_dir}/pid" "${_cpamp_upgrade_lock_dir}/pid.tmp" \
      "${_cpamp_upgrade_lock_dir}/token" "${_cpamp_upgrade_lock_dir}/token.tmp" \
      "${_cpamp_upgrade_lock_dir}/swapping" "${_cpamp_upgrade_lock_dir}/swapping.tmp" \
      2>/dev/null || true
    rmdir "${_cpamp_upgrade_lock_dir}" 2>/dev/null || rm -rf "${_cpamp_upgrade_lock_dir}" 2>/dev/null || true
  fi
  _cpamp_upgrade_lock_dir=""
}

lock_pid_is_alive() {
  local pid="$1"
  if kill -0 "${pid}" >/dev/null 2>&1; then
    return 0
  fi
  if ps -p "${pid}" -o pid= >/dev/null 2>&1; then
    return 0
  fi
  return 1
}

ensure_no_active_upgrade_lock() {
  local target_root="$1"
  local op_name="$2"
  local lock_dir="${target_root}/.upgrade.lock"
  local existing_pid="" existing_token="" ticks=0 lock_active=0

  if [ -L "${lock_dir}" ]; then
    echo "Refusing ${op_name}: unsafe symlinked upgrade lock ${lock_dir}" >&2
    exit 1
  fi
  if [ ! -e "${lock_dir}" ]; then
    return 0
  fi
  if [ ! -d "${lock_dir}" ]; then
    echo "Refusing ${op_name}: invalid upgrade lock path ${lock_dir}" >&2
    exit 1
  fi
  if [ -L "${lock_dir}/pid" ] || [ -L "${lock_dir}/token" ]; then
    echo "Refusing ${op_name}: unsafe symlinked upgrade lock metadata in ${lock_dir}" >&2
    exit 1
  fi
  if [ -f "${lock_dir}/pid" ]; then
    existing_pid="$(sed -n '1p' "${lock_dir}/pid" 2>/dev/null | tr -d '[:space:]' || true)"
  fi
  while [ -z "${existing_pid}" ] && [ -d "${lock_dir}" ] && [ "${ticks}" -lt 5 ]; do
    sleep 0.1
    ticks=$((ticks + 1))
    if [ -f "${lock_dir}/pid" ] && [ ! -L "${lock_dir}/pid" ]; then
      existing_pid="$(sed -n '1p' "${lock_dir}/pid" 2>/dev/null | tr -d '[:space:]' || true)"
    fi
  done
  if [ ! -d "${lock_dir}" ]; then
    return 0
  fi

  if [ -e "${lock_dir}/swapping" ] || [ -L "${lock_dir}/swapping" ]; then
    lock_active=1
  elif [ -z "${existing_pid}" ] || ! [[ "${existing_pid}" =~ ^[1-9][0-9]*$ ]]; then
    lock_active=1
  elif lock_pid_is_alive "${existing_pid}"; then
    lock_active=1
  fi

  if [ "${lock_active}" -eq 1 ]; then
    if [ -f "${lock_dir}/token" ] && [ ! -L "${lock_dir}/token" ]; then
      existing_token="$(sed -n '1p' "${lock_dir}/token" 2>/dev/null | tr -d '[:space:]' || true)"
    fi
    if [ -n "${CPAMP_UPGRADE_LOCK_TOKEN:-}" ] &&
      [ -n "${existing_token}" ] &&
      [ "${CPAMP_UPGRADE_LOCK_TOKEN}" = "${existing_token}" ]; then
      return 0
    fi
    echo "Refusing ${op_name}: an in-place upgrade is currently in progress at ${target_root}" >&2
    exit 1
  fi
  return 0
}

enter_upgrade_swap_phase() {
  local lock_dir="$1"
  [ -d "${lock_dir}" ] || return 0
  printf '%s\n' "$$" >"${lock_dir}/swapping.tmp"
  chmod 0600 "${lock_dir}/swapping.tmp" 2>/dev/null || true
  mv -f "${lock_dir}/swapping.tmp" "${lock_dir}/swapping"
}

leave_upgrade_swap_phase() {
  local lock_dir="$1"
  rm -f "${lock_dir}/swapping" "${lock_dir}/swapping.tmp" 2>/dev/null || true
}

acquire_upgrade_lock() {
  local lock_dir="$1"
  local wait_ticks=0 existing_pid rand_hex

  if [ -L "${lock_dir}" ]; then
    echo "Refusing upgrade: lock path ${lock_dir} is a symlink" >&2
    exit 1
  fi
  if [ -e "${lock_dir}" ] && [ ! -d "${lock_dir}" ]; then
    echo "Refusing upgrade: lock path ${lock_dir} is not a directory" >&2
    exit 1
  fi

  if ! mkdir "${lock_dir}" 2>/dev/null; then
    if [ -L "${lock_dir}" ] || { [ -e "${lock_dir}" ] && [ ! -d "${lock_dir}" ]; }; then
      echo "Refusing upgrade: unsafe lock path ${lock_dir}" >&2
      exit 1
    fi
    while [ -d "${lock_dir}" ] && [ ! -e "${lock_dir}/pid" ] && [ "${wait_ticks}" -lt 5 ]; do
      sleep 0.1
      wait_ticks=$((wait_ticks + 1))
    done
    if [ -d "${lock_dir}" ]; then
      if [ -L "${lock_dir}/pid" ] || [ ! -f "${lock_dir}/pid" ]; then
        echo "Refusing concurrent upgrade: lock ${lock_dir} exists without a verifiable PID file" >&2
        exit 1
      fi
      existing_pid="$(sed -n '1p' "${lock_dir}/pid" 2>/dev/null | tr -d '[:space:]' || true)"
      if ! [[ "${existing_pid}" =~ ^[1-9][0-9]*$ ]]; then
        echo "Refusing concurrent upgrade: lock ${lock_dir} has invalid PID record" >&2
        exit 1
      fi
      if lock_pid_is_alive "${existing_pid}"; then
        echo "Refusing concurrent upgrade: another upgrade is active (PID ${existing_pid}, lock ${lock_dir})" >&2
        exit 1
      fi
      if [ -e "${lock_dir}/swapping" ] || [ -L "${lock_dir}/swapping" ]; then
        echo "Refusing upgrade: incomplete upgrade swap lock detected at ${lock_dir}; manual recovery required" >&2
        exit 1
      fi
      echo "Clearing stale upgrade lock at ${lock_dir} (dead PID ${existing_pid})" >&2
      rm -f \
        "${lock_dir}/pid" "${lock_dir}/pid.tmp" \
        "${lock_dir}/token" "${lock_dir}/token.tmp" \
        "${lock_dir}/swapping" "${lock_dir}/swapping.tmp" \
        2>/dev/null || true
      rmdir "${lock_dir}" 2>/dev/null || {
        echo "Failed to clear stale upgrade lock at ${lock_dir}" >&2
        exit 1
      }
    fi
    if ! mkdir "${lock_dir}" 2>/dev/null; then
      echo "Refusing concurrent upgrade: failed to acquire exclusive lock ${lock_dir}" >&2
      exit 1
    fi
  fi

  chmod 0700 "${lock_dir}" 2>/dev/null || true
  _cpamp_upgrade_lock_dir="${lock_dir}"
  trap '_cpamp_cleanup_upgrade_dirs' EXIT
  rand_hex="$(dd if=/dev/urandom bs=8 count=1 2>/dev/null | od -An -tx1 | tr -d ' \n' || printf '0000')"
  _cpamp_upgrade_lock_token="$$:$(date +%s):${rand_hex}"
  printf '%s\n' "${_cpamp_upgrade_lock_token}" >"${lock_dir}/token.tmp"
  chmod 0600 "${lock_dir}/token.tmp" 2>/dev/null || true
  mv -f "${lock_dir}/token.tmp" "${lock_dir}/token"
  printf '%s\n' "$$" >"${lock_dir}/pid.tmp"
  chmod 0600 "${lock_dir}/pid.tmp" 2>/dev/null || true
  mv -f "${lock_dir}/pid.tmp" "${lock_dir}/pid"
}


_extract_json_string_field() {
  local json_file="$1"
  local field_name="$2"
  if [ -L "${json_file}" ] || [ ! -f "${json_file}" ]; then
    return 0
  fi
  sed -n "s/.*\"${field_name}\"[[:space:]]*:[[:space:]]*\"\\([^\"]*\\)\".*/\\1/p" "${json_file}" 2>/dev/null | head -n 1
}

_compare_decimal_digits() {
  local a="$1"
  local b="$2"
  if [ "${#a}" -lt "${#b}" ]; then
    printf '%s\n' "-1"
    return 0
  fi
  if [ "${#a}" -gt "${#b}" ]; then
    printf '%s\n' "1"
    return 0
  fi
  if [ "${a}" = "${b}" ]; then
    printf '%s\n' "0"
  elif [[ "${a}" < "${b}" ]]; then
    printf '%s\n' "-1"
  else
    printf '%s\n' "1"
  fi
}

# semver_v2_gte returns 0 (true) when SemVer 2.0 version $1 >= $2 (ignoring leading 'v' and '+build').
semver_v2_gte() {
  local raw_a="${1:-}"
  local raw_b="${2:-}"
  raw_a="${raw_a#v}"
  raw_b="${raw_b#v}"
  raw_a="${raw_a%%+*}"
  raw_b="${raw_b%%+*}"

  local core_a="${raw_a%%-*}"
  local pre_a=""
  if [[ "${raw_a}" == *-* ]]; then
    pre_a="${raw_a#*-}"
  fi
  local core_b="${raw_b%%-*}"
  local pre_b=""
  if [[ "${raw_b}" == *-* ]]; then
    pre_b="${raw_b#*-}"
  fi

  local IFS='.'
  local parts_a=() parts_b=()
  read -r -a parts_a <<<"${core_a}"
  read -r -a parts_b <<<"${core_b}"
  if [ "${#parts_a[@]}" -ne 3 ] || [ "${#parts_b[@]}" -ne 3 ]; then
    return 1
  fi
  local idx cmp_res
  for idx in 0 1 2; do
    if ! [[ "${parts_a[${idx}]}" =~ ^(0|[1-9][0-9]*)$ ]] || ! [[ "${parts_b[${idx}]}" =~ ^(0|[1-9][0-9]*)$ ]]; then
      return 1
    fi
    cmp_res="$(_compare_decimal_digits "${parts_a[${idx}]}" "${parts_b[${idx}]}")"
    if [ "${cmp_res}" = "1" ]; then
      return 0
    elif [ "${cmp_res}" = "-1" ]; then
      return 1
    fi
  done

  if [ -z "${pre_a}" ] && [ -z "${pre_b}" ]; then
    return 0
  fi
  if [ -z "${pre_a}" ]; then
    return 0
  fi
  if [ -z "${pre_b}" ]; then
    return 1
  fi

  local pre_parts_a=() pre_parts_b=()
  read -r -a pre_parts_a <<<"${pre_a}"
  read -r -a pre_parts_b <<<"${pre_b}"
  local limit="${#pre_parts_a[@]}"
  if [ "${#pre_parts_b[@]}" -gt "${limit}" ]; then
    limit="${#pre_parts_b[@]}"
  fi
  idx=0
  while [ "${idx}" -lt "${limit}" ]; do
    if [ "${idx}" -ge "${#pre_parts_a[@]}" ]; then
      return 1
    fi
    if [ "${idx}" -ge "${#pre_parts_b[@]}" ]; then
      return 0
    fi
    local pa="${pre_parts_a[${idx}]}"
    local pb="${pre_parts_b[${idx}]}"
    if [ "${pa}" != "${pb}" ]; then
      local a_num=0 b_num=0
      [[ "${pa}" =~ ^[0-9]+$ ]] && a_num=1
      [[ "${pb}" =~ ^[0-9]+$ ]] && b_num=1
      if [ "${a_num}" -eq 1 ] && [ "${b_num}" -eq 1 ]; then
        cmp_res="$(_compare_decimal_digits "${pa}" "${pb}")"
        [ "${cmp_res}" != "-1" ]
        return $?
      elif [ "${a_num}" -ne "${b_num}" ]; then
        [ "${a_num}" -eq 0 ]
        return $?
      else
        if [[ "${pa}" < "${pb}" ]]; then
          return 1
        fi
        return 0
      fi
    fi
    idx=$((idx + 1))
  done
  return 0
}
_systemd_unit_state=""
_systemd_load_state=""
_systemd_active_state=""
_systemd_sub_state=""

inspect_systemd_unit_state() {
  local sbin="$1"
  local sname="$2"
  local out="" rc=0 is_active_rc=0
  local parsed_lines=()
  local raw_l extra_idx
  local load_state active_state sub_state
  local restore_errexit=0

  case "$-" in *e*) restore_errexit=1 ;; esac

  _systemd_unit_state=""
  _systemd_load_state=""
  _systemd_active_state=""
  _systemd_sub_state=""

  set +e
  out="$("${sbin}" show -p LoadState,ActiveState,SubState --value "${sname}" 2>&1)"
  rc=$?
  [ "${restore_errexit}" -eq 1 ] && set -e
  if [ "${rc}" -ne 0 ]; then
    echo "Failed to query systemd state for ${sname}: ${out}" >&2
    exit 1
  fi

  while IFS= read -r raw_l || [ -n "${raw_l}" ]; do
    raw_l="${raw_l%$'\r'}"
    raw_l="${raw_l#"${raw_l%%[![:space:]]*}"}"
    raw_l="${raw_l%"${raw_l##*[![:space:]]}"}"
    parsed_lines+=("${raw_l}")
  done <<<"${out}"

  if [ "${#parsed_lines[@]}" -lt 3 ] ||
    [ -z "${parsed_lines[0]}" ] ||
    [ -z "${parsed_lines[1]}" ] ||
    [ -z "${parsed_lines[2]}" ]; then
    echo "Failed to query systemd state for ${sname}: unexpected systemctl show output: ${out}" >&2
    exit 1
  fi
  extra_idx=3
  while [ "${extra_idx}" -lt "${#parsed_lines[@]}" ]; do
    if [ -n "${parsed_lines[${extra_idx}]}" ]; then
      echo "Failed to query systemd state for ${sname}: unexpected extra output from systemctl show" >&2
      exit 1
    fi
    extra_idx=$((extra_idx + 1))
  done

  load_state="${parsed_lines[0]}"
  active_state="${parsed_lines[1]}"
  sub_state="${parsed_lines[2]}"
  _systemd_load_state="${load_state}"
  _systemd_active_state="${active_state}"
  _systemd_sub_state="${sub_state}"

  set +e
  "${sbin}" is-active --quiet "${sname}" >/dev/null 2>&1
  is_active_rc=$?
  [ "${restore_errexit}" -eq 1 ] && set -e

  if [ "${load_state}" = "not-found" ]; then
    if [ "${is_active_rc}" -eq 0 ]; then
      _systemd_unit_state="busy"
      return 0
    fi
    _systemd_unit_state="stopped"
    return 0
  fi

  if [ "${is_active_rc}" -eq 0 ]; then
    _systemd_unit_state="busy"
    return 0
  fi

  if [ "${active_state}" = "inactive" ] && [ "${sub_state}" = "dead" ]; then
    _systemd_unit_state="stopped"
    return 0
  fi

  if [ "${active_state}" = "failed" ] && [ "${sub_state}" != "auto-restart" ] && [ "${sub_state}" != "activating" ]; then
    _systemd_unit_state="stopped"
    return 0
  fi

  _systemd_unit_state="busy"
  return 0
}

systemctl_unit_is_active_healthy() {
  local sbin="$1"
  local sname="$2"
  local out="" rc=0 is_active_rc=0
  local parsed_lines=()
  local raw_l extra_idx
  local restore_errexit=0

  case "$-" in *e*) restore_errexit=1 ;; esac

  set +e
  "${sbin}" is-active --quiet "${sname}" >/dev/null 2>&1
  is_active_rc=$?
  out="$("${sbin}" show -p LoadState,ActiveState,SubState --value "${sname}" 2>&1)"
  rc=$?
  [ "${restore_errexit}" -eq 1 ] && set -e
  if [ "${is_active_rc}" -ne 0 ] || [ "${rc}" -ne 0 ]; then
    return 1
  fi

  while IFS= read -r raw_l || [ -n "${raw_l}" ]; do
    raw_l="${raw_l%$'\r'}"
    raw_l="${raw_l#"${raw_l%%[![:space:]]*}"}"
    raw_l="${raw_l%"${raw_l##*[![:space:]]}"}"
    parsed_lines+=("${raw_l}")
  done <<<"${out}"

  if [ "${#parsed_lines[@]}" -lt 3 ]; then
    return 1
  fi
  extra_idx=3
  while [ "${extra_idx}" -lt "${#parsed_lines[@]}" ]; do
    if [ -n "${parsed_lines[${extra_idx}]}" ]; then
      return 1
    fi
    extra_idx=$((extra_idx + 1))
  done

  [ "${parsed_lines[0]}" = "loaded" ] &&
    [ "${parsed_lines[1]}" = "active" ] &&
    [ "${parsed_lines[2]}" = "running" ]
}

wait_for_systemd_unit_ready() {
  local sbin="$1"
  local sname="$2"
  local target_run_dir="$3"
  local startup_timeout="${CPAMP_STARTUP_TIMEOUT_SEC:-30}"
  if ! [[ "${startup_timeout}" =~ ^[1-9][0-9]*$ ]]; then
    startup_timeout=30
  fi
  local max_ticks=$((startup_timeout * 10))
  local tick=0

  while [ "${tick}" -lt "${max_ticks}" ]; do
    if ! systemctl_unit_is_active_healthy "${sbin}" "${sname}"; then
      return 1
    fi
    if [ -f "${target_run_dir}/launcher.ready" ] && [ ! -L "${target_run_dir}/launcher.ready" ] &&
      (child_pid_if_active "${target_run_dir}/runtime-supervisor.pid") >/dev/null 2>&1 &&
      (child_pid_if_active "${target_run_dir}/manager.pid") >/dev/null 2>&1 &&
      (child_pid_if_active "${target_run_dir}/ingress.pid") >/dev/null 2>&1; then
      if systemctl_unit_is_active_healthy "${sbin}" "${sname}"; then
        return 0
      fi
      return 1
    fi
    sleep 0.1
    tick=$((tick + 1))
  done

  return 1
}

verify_staged_release_dir() {
  local sdir="$1"
  local sym entry req_rel req_abs mode

  sym="$(find "${sdir}" -type l -print 2>/dev/null | sed -n '1p')"
  if [ -n "${sym}" ]; then
    echo "Staged release verification failed: refusing symlink ${sym}" >&2
    exit 1
  fi

  local bin_count=0
  for entry in "${sdir}/bin/"*; do
    [ -e "${entry}" ] || continue
    if [ -L "${entry}" ] || [ ! -f "${entry}" ] || [ ! -x "${entry}" ]; then
      echo "Staged release verification failed: ${entry} must be a regular non-symlink executable" >&2
      exit 1
    fi
    mode="$(path_mode "${entry}")"
    if is_group_or_world_writable_mode "${mode}"; then
      echo "Staged release verification failed: ${entry} must not be group- or world-writable (mode ${mode})" >&2
      exit 1
    fi
    bin_count=$((bin_count + 1))
  done
  if [ "${bin_count}" -lt 5 ]; then
    echo "Staged release verification failed: incomplete bin directory in ${sdir}" >&2
    exit 1
  fi

  for req_rel in \
    bin/cpa-manager-plus \
    bin/cpa-manager-plus-ingress \
    bin/cpa-manager-plus-runtime-supervisor \
    bin/cli-proxy-api \
    bin/cpa-manager-plus-launcher \
    cpa-manager-plus-launcher \
    cpa-manager-plusctl; do
    req_abs="${sdir}/${req_rel}"
    if [ -L "${req_abs}" ] || [ ! -f "${req_abs}" ] || [ ! -x "${req_abs}" ]; then
      echo "Staged release verification failed: ${req_rel} must be a regular non-symlink executable" >&2
      exit 1
    fi
    mode="$(path_mode "${req_abs}")"
    if is_group_or_world_writable_mode "${mode}"; then
      echo "Staged release verification failed: ${req_rel} must not be group- or world-writable (mode ${mode})" >&2
      exit 1
    fi
  done

  for req_rel in \
    cpa-manager-plus.service \
    share/cpamp/provenance.json \
    share/cpamp/runtime/seed-lineage.json; do
    req_abs="${sdir}/${req_rel}"
    if [ -L "${req_abs}" ] || [ ! -f "${req_abs}" ] || [ ! -r "${req_abs}" ]; then
      echo "Staged release verification failed: ${req_rel} must be a regular readable non-symlink file" >&2
      exit 1
    fi
    if [ -x "${req_abs}" ]; then
      echo "Staged release verification failed: ${req_rel} must not have executable bits" >&2
      exit 1
    fi
    mode="$(path_mode "${req_abs}")"
    if is_group_or_world_writable_mode "${mode}"; then
      echo "Staged release verification failed: ${req_rel} must not be group- or world-writable (mode ${mode})" >&2
      exit 1
    fi
  done
}

upgrade_app() {
  local unit_dir="${CPA_MANAGER_PLUS_SYSTEMD_DIR:-/etc/systemd/system}"
  local service_name="${CPA_MANAGER_PLUS_SERVICE_NAME:-cpa-manager-plus.service}"
  local skip_systemctl="${CPA_MANAGER_PLUS_SKIP_SYSTEMCTL:-0}"
  local no_restart="0"
  local raw_target_home="/opt/cpa-manager-plus"
  local target_home

  while [ "$#" -gt 0 ]; do
    case "$1" in
      --home)
        [ "$#" -ge 2 ] || { echo "Missing value for --home" >&2; exit 1; }
        raw_target_home="$2"
        shift 2
        ;;
      --service-name)
        [ "$#" -ge 2 ] || { echo "Missing value for --service-name" >&2; exit 1; }
        service_name="$2"
        shift 2
        ;;
      --unit-dir)
        [ "$#" -ge 2 ] || { echo "Missing value for --unit-dir" >&2; exit 1; }
        unit_dir="$2"
        shift 2
        ;;
      --no-systemctl)
        skip_systemctl="1"
        shift
        ;;
      --no-restart)
        no_restart="1"
        shift
        ;;
      *)
        echo "Unknown upgrade option: $1" >&2
        exit 1
        ;;
    esac
  done

  if [ "$(id -u)" -ne 0 ] && [ "${CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE:-0}" != "1" ]; then
    echo "Refusing to run upgrade without root privileges; re-run via sudo (e.g. sudo $0 upgrade --home ${raw_target_home})." >&2
    exit 1
  fi

  case "${service_name}" in
    *.service) ;;
    *) service_name="${service_name}.service" ;;
  esac
  require_install_service_name "${service_name}"
  require_absolute_install_path "unit directory" "${unit_dir}"
  require_install_home "${raw_target_home}"
  while [ "${raw_target_home}" != "/" ] && [ "${raw_target_home%/}" != "${raw_target_home}" ]; do
    raw_target_home="${raw_target_home%/}"
  done
  if [ "${raw_target_home}" = "/" ]; then
    echo "Refusing to upgrade with install home set to /" >&2
    exit 1
  fi
  case "${raw_target_home}/" in
    *//* | */./* | */../*)
      echo "Refusing unsafe target home path with unexpected components: ${raw_target_home}" >&2
      exit 1
      ;;
  esac
  if [ -L "${raw_target_home}" ] || [ ! -d "${raw_target_home}" ]; then
    echo "Target installation directory does not exist or is a symlink: ${raw_target_home}" >&2
    exit 1
  fi

  target_home="$(resolve_path "${raw_target_home}")"
  while [ "${target_home}" != "/" ] && [ "${target_home%/}" != "${target_home}" ]; do
    target_home="${target_home%/}"
  done
  require_install_home "${target_home}"
  if [ "${target_home}" = "/" ] || [ -L "${target_home}" ] || [ ! -d "${target_home}" ]; then
    echo "Target installation directory does not exist or is a symlink: ${target_home}" >&2
    exit 1
  fi
  case "${target_home}/" in
    *//* | */./* | */../*)
      echo "Refusing unsafe resolved target home path: ${target_home}" >&2
      exit 1
      ;;
  esac
  case "${target_home}" in
    / | /bin | /boot | /dev | /etc | /lib | /lib64 | /opt | /proc | /sbin | /sys | /tmp | /usr | /var)
      echo "Refusing upgrade into system directory: ${target_home}" >&2
      exit 1
      ;;
  esac

  local check_prefix="" seg saved_ifs="${IFS}"
  IFS='/'
  for seg in ${target_home#/}; do
    if [ -z "${seg}" ] || [ "${seg}" = "." ] || [ "${seg}" = ".." ]; then
      IFS="${saved_ifs}"
      echo "Refusing unexpected path component (${seg}) in ${target_home}" >&2
      exit 1
    fi
    check_prefix="${check_prefix}/${seg}"
    if [ -L "${check_prefix}" ] || [ ! -d "${check_prefix}" ]; then
      IFS="${saved_ifs}"
      echo "Refusing target home with symlink or non-directory component: ${check_prefix}" >&2
      exit 1
    fi
  done
  IFS="${saved_ifs}"

  local has_ctl_marker=0 has_v2_manager=0 has_v2_ingress=0 has_v2_supervisor=0
  if [ -f "${target_home}/cpa-manager-plusctl" ] && [ ! -L "${target_home}/cpa-manager-plusctl" ]; then
    has_ctl_marker=1
  fi
  if [ -d "${target_home}/bin" ] && [ ! -L "${target_home}/bin" ]; then
    if [ -f "${target_home}/bin/cpa-manager-plus" ] && [ ! -L "${target_home}/bin/cpa-manager-plus" ]; then
      has_v2_manager=1
    fi
    if [ -f "${target_home}/bin/cpa-manager-plus-ingress" ] && [ ! -L "${target_home}/bin/cpa-manager-plus-ingress" ]; then
      has_v2_ingress=1
    fi
    if [ -f "${target_home}/bin/cpa-manager-plus-runtime-supervisor" ] && [ ! -L "${target_home}/bin/cpa-manager-plus-runtime-supervisor" ]; then
      has_v2_supervisor=1
    fi
  fi
  if [ "${has_ctl_marker}" -eq 0 ] || [ "${has_v2_manager}" -eq 0 ] ||
    [ "${has_v2_ingress}" -eq 0 ] || [ "${has_v2_supervisor}" -eq 0 ]; then
    if { [ -e "${target_home}/cpa-manager-plus" ] || [ -L "${target_home}/cpa-manager-plus" ] ||
      [ -e "${target_home}/run.sh" ] || [ -L "${target_home}/run.sh" ]; } &&
      { [ "${has_v2_manager}" -eq 0 ] || [ "${has_v2_ingress}" -eq 0 ] || [ "${has_v2_supervisor}" -eq 0 ]; }; then
      echo "Refusing in-place upgrade: ${target_home} appears to be a 1.x Native installation; run the 1.x -> v2 migration workflow instead" >&2
      exit 1
    fi
    echo "Refusing upgrade: ${target_home} does not look like an existing CPAMP Native installation (missing regular non-symlink v2 files in ${target_home})." >&2
    exit 1
  fi

  if [ "$(id -u)" -eq 0 ]; then
    local target_stat target_uid target_gid target_mode
    target_stat="$(path_owner_and_mode "${target_home}")" || {
      echo "Failed to inspect ownership and permissions of ${target_home}" >&2
      exit 1
    }
    IFS=':' read -r target_uid target_gid target_mode <<<"${target_stat}"
    if [ "${target_uid}" != "0" ] || [ "${target_gid}" != "0" ]; then
      echo "Refusing upgrade: ${target_home} must be owned by 0:0 (found ${target_uid}:${target_gid})" >&2
      exit 1
    fi
    if is_group_or_world_writable_mode "${target_mode}"; then
      echo "Refusing upgrade: ${target_home} must not be group- or world-writable (mode ${target_mode})" >&2
      exit 1
    fi
  fi

  local source_dir
  source_dir="$(resolve_path "${script_dir}")"
  case "${source_dir}" in
    "${target_home}" | "${target_home}"/*)
      echo "Refusing upgrade from inside target installation (${source_dir}); extract the new release outside ${target_home} first." >&2
      exit 1
      ;;
  esac
  case "${target_home}" in
    "${source_dir}" | "${source_dir}"/*)
      echo "Refusing upgrade into a subdirectory of the source bundle (${target_home} inside ${source_dir})." >&2
      exit 1
      ;;
  esac

  if [ -L "${unit_dir}" ]; then
    echo "Refusing symlinked unit directory: ${unit_dir}" >&2
    exit 1
  fi
  local unit_path="${unit_dir}/${service_name}"
  if [ -L "${unit_path}" ]; then
    echo "Refusing symlinked systemd unit path: ${unit_path}" >&2
    exit 1
  fi
  if [ -e "${unit_path}" ] && [ ! -f "${unit_path}" ]; then
    echo "Refusing non-regular systemd unit path: ${unit_path}" >&2
    exit 1
  fi

  local parsed_HTTP_ADDR="" parsed_CPAMP_INGRESS_ADDR="" parsed_USAGE_DATA_DIR=""
  local parsed_CPA_MANAGER_PLUS_DATA_DIR="" parsed_USAGE_DB_PATH=""
  local parsed_CPA_MANAGER_DATA_KEY_PATH="" parsed_CPA_MANAGER_PLUS_RUN_DIR=""
  local parsed_CPA_MANAGER_PLUS_LOG_DIR="" parsed_CPAMP_RUNTIME_TOKEN_FILE=""
  local parsed_CPAMP_CPA_EXECUTABLE="" parsed_CPAMP_RUNTIME_ADDR=""
  local parsed_CPAMP_MANAGER_ADDR="" parsed_CPAMP_INGRESS_CONTROL_ADDR=""
  local parsed_CPAMP_RUNTIME_CPA_ADDR="" parsed_CPAMP_CPA_PORT=""
  local parsed_CPAMP_GATEWAY_URL="" parsed_CPA_UPSTREAM_URL=""
  local parsed_CPAMP_DRAIN_TIMEOUT="" parsed_CPAMP_RUNTIME_CPA_UID=""
  local parsed_CPAMP_RUNTIME_CPA_GID="" parsed_CPAMP_CPA_USER=""
  local parsed_CPAMP_RUNTIME_DATA_DIR="" parsed_CPAMP_RUNTIME_JOURNAL_PATH=""
  local parsed_CPA_MANAGER_PLUS_LAUNCHER=""
  local preserved_env_files="" preserved_rw_paths=""
  local merged_env_entries=()
  local merged_env_files=()
  local _preserve_env_files=()
  local _preserve_rw_paths=()

  _set_parsed_unit_var() {
    case "$1" in
      HTTP_ADDR) parsed_HTTP_ADDR="$2" ;;
      CPAMP_INGRESS_ADDR) parsed_CPAMP_INGRESS_ADDR="$2" ;;
      USAGE_DATA_DIR) parsed_USAGE_DATA_DIR="$2" ;;
      CPA_MANAGER_PLUS_DATA_DIR) parsed_CPA_MANAGER_PLUS_DATA_DIR="$2" ;;
      USAGE_DB_PATH) parsed_USAGE_DB_PATH="$2" ;;
      CPA_MANAGER_DATA_KEY_PATH) parsed_CPA_MANAGER_DATA_KEY_PATH="$2" ;;
      CPA_MANAGER_PLUS_RUN_DIR) parsed_CPA_MANAGER_PLUS_RUN_DIR="$2" ;;
      CPA_MANAGER_PLUS_LOG_DIR) parsed_CPA_MANAGER_PLUS_LOG_DIR="$2" ;;
      CPAMP_RUNTIME_TOKEN_FILE) parsed_CPAMP_RUNTIME_TOKEN_FILE="$2" ;;
      CPAMP_CPA_EXECUTABLE) parsed_CPAMP_CPA_EXECUTABLE="$2" ;;
      CPAMP_RUNTIME_ADDR) parsed_CPAMP_RUNTIME_ADDR="$2" ;;
      CPAMP_MANAGER_ADDR) parsed_CPAMP_MANAGER_ADDR="$2" ;;
      CPAMP_INGRESS_CONTROL_ADDR) parsed_CPAMP_INGRESS_CONTROL_ADDR="$2" ;;
      CPAMP_RUNTIME_CPA_ADDR) parsed_CPAMP_RUNTIME_CPA_ADDR="$2" ;;
      CPAMP_CPA_PORT) parsed_CPAMP_CPA_PORT="$2" ;;
      CPAMP_GATEWAY_URL) parsed_CPAMP_GATEWAY_URL="$2" ;;
      CPA_UPSTREAM_URL) parsed_CPA_UPSTREAM_URL="$2" ;;
      CPAMP_DRAIN_TIMEOUT) parsed_CPAMP_DRAIN_TIMEOUT="$2" ;;
      CPAMP_RUNTIME_CPA_UID) parsed_CPAMP_RUNTIME_CPA_UID="$2" ;;
      CPAMP_RUNTIME_CPA_GID) parsed_CPAMP_RUNTIME_CPA_GID="$2" ;;
      CPAMP_CPA_USER) parsed_CPAMP_CPA_USER="$2" ;;
      CPAMP_RUNTIME_DATA_DIR) parsed_CPAMP_RUNTIME_DATA_DIR="$2" ;;
      CPAMP_RUNTIME_JOURNAL_PATH) parsed_CPAMP_RUNTIME_JOURNAL_PATH="$2" ;;
    esac
  }

  local effective_unit_file=""
  if [ -f "${unit_path}" ]; then
    effective_unit_file="${unit_path}"
  elif [ "${unit_dir}" = "/etc/systemd/system" ]; then
    local alt_unit
    for alt_unit in \
      "/run/systemd/system/${service_name}" \
      "/usr/lib/systemd/system/${service_name}" \
      "/lib/systemd/system/${service_name}"; do
      if [ -L "${alt_unit}" ]; then
        echo "Refusing symlinked systemd unit path: ${alt_unit}" >&2
        exit 1
      fi
      if [ -f "${alt_unit}" ]; then
        effective_unit_file="${alt_unit}"
        break
      fi
    done
  fi

  local dropin_dirs=("${unit_path}.d")
  case "${unit_dir}" in
    /etc/systemd/system | /lib/systemd/system | /usr/lib/systemd/system | /run/systemd/system)
      if [ "${unit_dir}" != "/etc/systemd/system" ]; then
        dropin_dirs+=("/etc/systemd/system/${service_name}.d")
      fi
      if [ "${unit_dir}" != "/run/systemd/system" ]; then
        dropin_dirs+=("/run/systemd/system/${service_name}.d")
      fi
      ;;
  esac

  local dropin_files=()
  local d_dir d_conf
  local saved_lc_all="${LC_ALL-}" had_lc_all=0
  [ "${LC_ALL+x}" = "x" ] && had_lc_all=1
  LC_ALL=C
  for d_dir in "${dropin_dirs[@]}"; do
    if [ -L "${d_dir}" ]; then
      echo "Refusing symlinked systemd drop-in directory: ${d_dir}" >&2
      exit 1
    fi
    [ -d "${d_dir}" ] || continue
    for d_conf in "${d_dir}"/*.conf; do
      [ -e "${d_conf}" ] || [ -L "${d_conf}" ] || continue
      if [ -L "${d_conf}" ] || [ ! -f "${d_conf}" ]; then
        echo "Refusing unsafe systemd drop-in file: ${d_conf}" >&2
        exit 1
      fi
      dropin_files+=("${d_conf}")
    done
  done
  if [ "${had_lc_all}" -eq 1 ]; then
    export LC_ALL="${saved_lc_all}"
  else
    unset LC_ALL
  fi

  local unit_working_dir_seen=0 unit_working_dir_val=""
  local unit_exec_start_count=0 unit_exec_start_val=""
  local unit_user_val="" unit_group_val=""

  _parse_systemd_service_file() {
    local unit_src="$1"
    local is_base_unit="$2"
    local in_service=0 raw_line tab_scrubbed trimmed_line
    local env_rhs token_kv k v exec_rhs wd_rhs ef_rhs ef_tok ef_check rw_rhs rw_tok rw_check rw_entry optional_prefix
    local u_val g_val
    local restore_noglob=0
    local already_seen=0 existing_entry

    while IFS= read -r raw_line || [ -n "${raw_line}" ]; do
      raw_line="${raw_line%$'\r'}"
      tab_scrubbed="${raw_line//$'\t'/ }"
      case "${tab_scrubbed}" in
        *[[:cntrl:]]*)
          echo "Refusing upgrade: control character in systemd file ${unit_src}" >&2
          exit 1
          ;;
      esac
      trimmed_line="${raw_line#"${raw_line%%[![:space:]]*}"}"
      trimmed_line="${trimmed_line%"${trimmed_line##*[![:space:]]}"}"
      case "${trimmed_line}" in
        '' | '#'* | ';'*) continue ;;
        '[Service]') in_service=1; continue ;;
        '['*']') in_service=0; continue ;;
      esac
      [ "${in_service}" -eq 1 ] || continue

      if [[ "${trimmed_line}" == *'\' ]]; then
        echo "Refusing upgrade: unsupported multiline line continuation in ${unit_src}: ${trimmed_line}" >&2
        exit 1
      fi

      if [[ "${trimmed_line}" =~ ^EnvironmentFile[[:space:]]*=(.*)$ ]]; then
        ef_rhs="${BASH_REMATCH[1]}"
        ef_rhs="${ef_rhs#"${ef_rhs%%[![:space:]]*}"}"
        ef_rhs="${ef_rhs%"${ef_rhs##*[![:space:]]}"}"
        case "${ef_rhs}" in
          *'"'* | *"'"* | *'\'* | *'$'* | *'%'* | *[[:cntrl:]]*)
            echo "Refusing upgrade: EnvironmentFile in ${unit_src} uses unsupported quoting or special characters: ${ef_rhs}" >&2
            exit 1
            ;;
        esac
        if [ -z "${ef_rhs}" ]; then
          merged_env_files=()
          _preserve_env_files=()
        else
          restore_noglob=0
          case "$-" in *f*) restore_noglob=1 ;; esac
          set -f
          for ef_tok in ${ef_rhs}; do
            ef_check="${ef_tok#-}"
            require_absolute_install_path "systemd EnvironmentFile path" "${ef_check}"
            merged_env_files+=("${ef_tok}")
            if [ "${is_base_unit}" -eq 1 ]; then
              already_seen=0
              if [ "${#_preserve_env_files[@]}" -gt 0 ]; then
                for existing_entry in "${_preserve_env_files[@]}"; do
                  if [ "${existing_entry}" = "${ef_tok}" ]; then
                    already_seen=1
                    break
                  fi
                done
              fi
              if [ "${already_seen}" -eq 0 ]; then
                _preserve_env_files+=("${ef_tok}")
              fi
            fi
          done
          [ "${restore_noglob}" -eq 0 ] && set +f
        fi
      elif [[ "${trimmed_line}" =~ ^Environment[[:space:]]*=(.*)$ ]]; then
        env_rhs="${BASH_REMATCH[1]}"
        env_rhs="${env_rhs#"${env_rhs%%[![:space:]]*}"}"
        env_rhs="${env_rhs%"${env_rhs##*[![:space:]]}"}"
        if [ -z "${env_rhs}" ]; then
          merged_env_entries=()
        else
          while true; do
            env_rhs="${env_rhs#"${env_rhs%%[![:space:]]*}"}"
            [ -n "${env_rhs}" ] || break
            if [[ "${env_rhs}" == \"* ]]; then
              env_rhs="${env_rhs:1}"
              if [[ "${env_rhs}" == *\"* ]]; then
                token_kv="${env_rhs%%\"*}"
                env_rhs="${env_rhs#*\"}"
              else
                token_kv="${env_rhs}"
                env_rhs=""
              fi
            elif [[ "${env_rhs}" == \'* ]]; then
              env_rhs="${env_rhs:1}"
              if [[ "${env_rhs}" == *\'* ]]; then
                token_kv="${env_rhs%%\'*}"
                env_rhs="${env_rhs#*\'}"
              else
                token_kv="${env_rhs}"
                env_rhs=""
              fi
            else
              token_kv="${env_rhs%%[[:space:]]*}"
              if [ "${token_kv}" = "${env_rhs}" ]; then
                env_rhs=""
              else
                env_rhs="${env_rhs#*[[:space:]]}"
              fi
            fi
            case "${token_kv}" in
              *=*)
                k="${token_kv%%=*}"
                v="${token_kv#*=}"
                merged_env_entries+=("${k}=${v}")
                ;;
            esac
          done
        fi
      elif [[ "${trimmed_line}" =~ ^ReadWritePaths[[:space:]]*=(.*)$ ]]; then
        rw_rhs="${BASH_REMATCH[1]}"
        rw_rhs="${rw_rhs#"${rw_rhs%%[![:space:]]*}"}"
        rw_rhs="${rw_rhs%"${rw_rhs##*[![:space:]]}"}"
        case "${rw_rhs}" in
          *'"'* | *"'"* | *'\'* | *'$'* | *'%'* | *[[:cntrl:]]*)
            echo "Refusing upgrade: ReadWritePaths in ${unit_src} uses unsupported quoting or special characters that cannot be safely preserved: ${rw_rhs}" >&2
            exit 1
            ;;
        esac
        if [ -z "${rw_rhs}" ]; then
          _preserve_rw_paths=()
        else
          restore_noglob=0
          case "$-" in *f*) restore_noglob=1 ;; esac
          set -f
          for rw_tok in ${rw_rhs}; do
            optional_prefix=""
            rw_check="${rw_tok}"
            if [[ "${rw_tok}" == -* ]]; then
              optional_prefix="-"
              rw_check="${rw_tok#-}"
            fi
            case "${rw_check}" in
              /*) ;;
              *)
                [ "${restore_noglob}" -eq 0 ] && set +f
                echo "Refusing upgrade: ReadWritePaths entry in ${unit_src} must be an absolute path: ${rw_tok}" >&2
                exit 1
                ;;
            esac
            require_safe_unit_value "ReadWritePaths entry" "${rw_tok}"
            rw_entry="${optional_prefix}${rw_check}"
            already_seen=0
            if [ "${#_preserve_rw_paths[@]}" -gt 0 ]; then
              for existing_entry in "${_preserve_rw_paths[@]}"; do
                if [ "${existing_entry}" = "${rw_entry}" ]; then
                  already_seen=1
                  break
                fi
              done
            fi
            if [ "${already_seen}" -eq 0 ]; then
              _preserve_rw_paths+=("${rw_entry}")
            fi
          done
          [ "${restore_noglob}" -eq 0 ] && set +f
        fi
      elif [[ "${trimmed_line}" =~ ^WorkingDirectory[[:space:]]*=(.*)$ ]]; then
        wd_rhs="${BASH_REMATCH[1]}"
        wd_rhs="${wd_rhs#"${wd_rhs%%[![:space:]]*}"}"
        wd_rhs="${wd_rhs%"${wd_rhs##*[![:space:]]}"}"
        unit_working_dir_seen=1
        if [ -z "${wd_rhs}" ]; then
          unit_working_dir_val=""
        else
          wd_rhs="${wd_rhs#-}"
          if [[ "${wd_rhs}" == \"*\" ]] && [ "${#wd_rhs}" -ge 2 ]; then
            wd_rhs="${wd_rhs#\"}"
            wd_rhs="${wd_rhs%\"}"
          fi
          case "${wd_rhs}" in
            *'"'* | *"'"* | *'\'* | *'$'* | *'%'* | *[[:space:]]* | *[[:cntrl:]]*)
              echo "Refusing upgrade: WorkingDirectory in ${unit_src} uses unsupported quoting or special characters: ${wd_rhs}" >&2
              exit 1
              ;;
          esac
          unit_working_dir_val="${wd_rhs}"
        fi
      elif [[ "${trimmed_line}" =~ ^ExecStart[[:space:]]*=(.*)$ ]]; then
        exec_rhs="${BASH_REMATCH[1]}"
        exec_rhs="${exec_rhs#"${exec_rhs%%[![:space:]]*}"}"
        exec_rhs="${exec_rhs%"${exec_rhs##*[![:space:]]}"}"
        if [ -z "${exec_rhs}" ]; then
          unit_exec_start_val=""
          unit_exec_start_count=0
        else
          case "${exec_rhs}" in
            *'$'* | *'%'* | *[[:cntrl:]]*)
              echo "Refusing upgrade: ExecStart in ${unit_src} uses unsupported specifiers or control characters: ${exec_rhs}" >&2
              exit 1
              ;;
          esac
          while [[ "${exec_rhs}" =~ ^[-@+!:] ]]; do
            exec_rhs="${exec_rhs:1}"
          done
          exec_rhs="${exec_rhs#"${exec_rhs%%[![:space:]]*}"}"
          if [[ "${exec_rhs}" == \"* ]]; then
            exec_rhs="${exec_rhs:1}"
            if [[ "${exec_rhs}" != *\"* ]]; then
              echo "Refusing upgrade: unterminated quoted ExecStart in ${unit_src}" >&2
              exit 1
            fi
            exec_rhs="${exec_rhs%%\"*}"
          else
            exec_rhs="${exec_rhs%%[[:space:]]*}"
          fi
          unit_exec_start_count=$((unit_exec_start_count + 1))
          unit_exec_start_val="${exec_rhs}"
        fi
      elif [[ "${trimmed_line}" =~ ^User[[:space:]]*=(.*)$ ]]; then
        u_val="${BASH_REMATCH[1]}"
        u_val="${u_val#"${u_val%%[![:space:]]*}"}"
        u_val="${u_val%"${u_val##*[![:space:]]}"}"
        unit_user_val="${u_val}"
      elif [[ "${trimmed_line}" =~ ^Group[[:space:]]*=(.*)$ ]]; then
        g_val="${BASH_REMATCH[1]}"
        g_val="${g_val#"${g_val%%[![:space:]]*}"}"
        g_val="${g_val%"${g_val##*[![:space:]]}"}"
        unit_group_val="${g_val}"
      fi
    done <"${unit_src}"
  }

  if [ -n "${effective_unit_file}" ]; then
    _parse_systemd_service_file "${effective_unit_file}" 1
  elif [ -f "${target_home}/cpa-manager-plus.env" ] && [ ! -L "${target_home}/cpa-manager-plus.env" ]; then
    merged_env_files+=("${target_home}/cpa-manager-plus.env")
  fi

  if [ "${#dropin_files[@]}" -gt 0 ]; then
    local df_path
    for df_path in "${dropin_files[@]}"; do
      _parse_systemd_service_file "${df_path}" 0
    done
  fi

  if [ "${#merged_env_entries[@]}" -gt 0 ]; then
    local env_entry
    for env_entry in "${merged_env_entries[@]}"; do
      _set_parsed_unit_var "${env_entry%%=*}" "${env_entry#*=}"
    done
  fi

  if [ "${#merged_env_files[@]}" -gt 0 ]; then
    local ef_entry ef_optional ef_path
    for ef_entry in "${merged_env_files[@]}"; do
      ef_optional=0
      ef_path="${ef_entry}"
      if [[ "${ef_entry}" == -* ]]; then
        ef_optional=1
        ef_path="${ef_entry#-}"
      fi
      if [ -L "${ef_path}" ]; then
        echo "Refusing upgrade: symlinked EnvironmentFile ${ef_path}" >&2
        exit 1
      fi
      if [ -e "${ef_path}" ] && [ ! -f "${ef_path}" ]; then
        echo "Refusing upgrade: non-regular EnvironmentFile ${ef_path}" >&2
        exit 1
      fi
      if [ -f "${ef_path}" ]; then
        _parse_env_file_kv "${ef_path}" "_set_parsed_unit_var"
      elif [ "${ef_optional}" -eq 0 ]; then
        echo "Refusing upgrade: missing required EnvironmentFile ${ef_path}" >&2
        exit 1
      fi
    done
  fi

  if [ "${#_preserve_env_files[@]}" -gt 0 ]; then
    preserved_env_files="$(printf '%s\n' "${_preserve_env_files[@]}")"
  fi
  if [ "${#_preserve_rw_paths[@]}" -gt 0 ]; then
    preserved_rw_paths="$(printf '%s\n' "${_preserve_rw_paths[@]}")"
  fi

  if [ -n "${unit_user_val}" ] && [ "${unit_user_val}" != "root" ] && [ "${unit_user_val}" != "0" ]; then
    echo "Refusing upgrade: systemd unit configures User=${unit_user_val}; v2 native launcher must run as root" >&2
    exit 1
  fi
  if [ -n "${unit_group_val}" ] && [ "${unit_group_val}" != "root" ] && [ "${unit_group_val}" != "0" ]; then
    echo "Refusing upgrade: systemd unit configures Group=${unit_group_val}; v2 native launcher must run as root" >&2
    exit 1
  fi

  if [ -n "${effective_unit_file}" ] || [ "${unit_working_dir_seen}" -eq 1 ]; then
    if [ "${unit_working_dir_seen}" -eq 0 ] || [ -z "${unit_working_dir_val}" ]; then
      echo "Refusing upgrade: systemd unit ${effective_unit_file:-drop-in} is missing WorkingDirectory=${target_home}" >&2
      exit 1
    fi
    if [ -L "${unit_working_dir_val}" ]; then
      echo "Refusing upgrade: systemd WorkingDirectory must not be a symlink (${unit_working_dir_val})" >&2
      exit 1
    fi
    local wd_resolved="${unit_working_dir_val}"
    if [ -d "${wd_resolved}" ]; then
      wd_resolved="$(resolve_path "${wd_resolved}")"
    fi
    while [ "${wd_resolved}" != "/" ] && [ "${wd_resolved%/}" != "${wd_resolved}" ]; do
      wd_resolved="${wd_resolved%/}"
    done
    if [ "${wd_resolved}" != "${target_home}" ]; then
      echo "Refusing upgrade: systemd unit WorkingDirectory (${unit_working_dir_val}) does not match target installation ${target_home}" >&2
      exit 1
    fi
  fi

  if [ -n "${effective_unit_file}" ] || [ "${unit_exec_start_count}" -gt 0 ]; then
    if [ "${unit_exec_start_count}" -gt 1 ]; then
      echo "Refusing upgrade: systemd unit ${effective_unit_file:-drop-in} defines multiple ExecStart directives" >&2
      exit 1
    fi
    if [ -z "${unit_exec_start_val}" ]; then
      echo "Refusing upgrade: systemd unit ${effective_unit_file:-drop-in} is missing ExecStart for ${target_home}" >&2
      exit 1
    fi
    if [ -L "${unit_exec_start_val}" ]; then
      echo "Refusing upgrade: systemd ExecStart must not be a symlink (${unit_exec_start_val})" >&2
      exit 1
    fi
    local exec_dir exec_base exec_resolved="${unit_exec_start_val}"
    exec_dir="$(dirname "${unit_exec_start_val}")"
    exec_base="$(basename "${unit_exec_start_val}")"
    if [ -d "${exec_dir}" ]; then
      exec_resolved="$(resolve_path "${exec_dir}")/${exec_base}"
    fi
    if [ "${exec_resolved}" != "${target_home}/bin/cpa-manager-plus-launcher" ] &&
      [ "${exec_resolved}" != "${target_home}/cpa-manager-plus-launcher" ]; then
      echo "Refusing upgrade: systemd unit ExecStart (${unit_exec_start_val}) does not belong to target installation ${target_home}" >&2
      exit 1
    fi
    if [ "${exec_resolved}" = "${target_home}/cpa-manager-plus-launcher" ]; then
      parsed_CPA_MANAGER_PLUS_LAUNCHER="${target_home}/cpa-manager-plus-launcher"
    fi
  fi

  local eff_data_dir eff_run_dir eff_log_dir eff_cpa_bin eff_cpa_uid eff_cpa_addr eff_pid_file
  local eff_runtime_data_dir eff_journal_path eff_cpa_user
  eff_data_dir="${USAGE_DATA_DIR:-"${CPA_MANAGER_PLUS_DATA_DIR:-"${parsed_USAGE_DATA_DIR:-"${parsed_CPA_MANAGER_PLUS_DATA_DIR:-"${target_home}/data"}"}"}"}"
  eff_run_dir="${CPA_MANAGER_PLUS_RUN_DIR:-"${parsed_CPA_MANAGER_PLUS_RUN_DIR:-"${target_home}/run"}"}"
  eff_log_dir="${CPA_MANAGER_PLUS_LOG_DIR:-"${parsed_CPA_MANAGER_PLUS_LOG_DIR:-"${target_home}/logs"}"}"
  eff_cpa_bin="${CPAMP_CPA_EXECUTABLE:-"${parsed_CPAMP_CPA_EXECUTABLE:-"${target_home}/bin/cli-proxy-api"}"}"
  eff_cpa_uid="${CPAMP_RUNTIME_CPA_UID:-"${parsed_CPAMP_RUNTIME_CPA_UID:-}"}"
  eff_cpa_user="${CPAMP_CPA_USER:-"${parsed_CPAMP_CPA_USER:-}"}"
  eff_runtime_data_dir="${CPAMP_RUNTIME_DATA_DIR:-"${parsed_CPAMP_RUNTIME_DATA_DIR:-}"}"
  eff_journal_path="${CPAMP_RUNTIME_JOURNAL_PATH:-"${parsed_CPAMP_RUNTIME_JOURNAL_PATH:-}"}"
  if [ -n "${CPAMP_RUNTIME_CPA_ADDR:-}" ] || [ -n "${CPAMP_CPA_PORT:-}" ] ||
    [ -n "${CPAMP_GATEWAY_URL:-}" ] || [ -n "${CPA_UPSTREAM_URL:-}" ]; then
    eff_cpa_addr="$(cpa_address_from_environment)"
  elif [ -n "${parsed_CPAMP_RUNTIME_CPA_ADDR}" ] || [ -n "${parsed_CPAMP_CPA_PORT}" ] ||
    [ -n "${parsed_CPAMP_GATEWAY_URL}" ] || [ -n "${parsed_CPA_UPSTREAM_URL}" ]; then
    eff_cpa_addr="$(
      CPAMP_RUNTIME_CPA_ADDR="${parsed_CPAMP_RUNTIME_CPA_ADDR}" \
        CPAMP_CPA_PORT="${parsed_CPAMP_CPA_PORT}" \
        CPAMP_GATEWAY_URL="${parsed_CPAMP_GATEWAY_URL}" \
        CPA_UPSTREAM_URL="${parsed_CPA_UPSTREAM_URL}" \
        cpa_address_from_environment
    )"
  else
    eff_cpa_addr="127.0.0.1:8317"
  fi
  eff_pid_file="${CPA_MANAGER_PLUS_PID_FILE:-"${eff_run_dir}/${app_name}.pid"}"

  run_with_target_unit_env() {
    (
      export CPA_MANAGER_PLUS_HOME="${target_home}"
      if [ -z "${HTTP_ADDR+x}" ] && [ -z "${CPAMP_INGRESS_ADDR+x}" ]; then
        if [ -n "${parsed_HTTP_ADDR}" ]; then
          export HTTP_ADDR="${parsed_HTTP_ADDR}"
        elif [ -n "${parsed_CPAMP_INGRESS_ADDR}" ]; then
          export CPAMP_INGRESS_ADDR="${parsed_CPAMP_INGRESS_ADDR}"
        fi
      fi
      if [ -z "${USAGE_DATA_DIR+x}" ] && [ -z "${CPA_MANAGER_PLUS_DATA_DIR+x}" ]; then
        if [ -n "${parsed_USAGE_DATA_DIR}" ]; then
          export USAGE_DATA_DIR="${parsed_USAGE_DATA_DIR}"
        elif [ -n "${parsed_CPA_MANAGER_PLUS_DATA_DIR}" ]; then
          export CPA_MANAGER_PLUS_DATA_DIR="${parsed_CPA_MANAGER_PLUS_DATA_DIR}"
        fi
      fi
      if [ -z "${USAGE_DB_PATH+x}" ] && [ -n "${parsed_USAGE_DB_PATH}" ]; then
        export USAGE_DB_PATH="${parsed_USAGE_DB_PATH}"
      fi
      if [ -z "${CPA_MANAGER_DATA_KEY_PATH+x}" ] && [ -n "${parsed_CPA_MANAGER_DATA_KEY_PATH}" ]; then
        export CPA_MANAGER_DATA_KEY_PATH="${parsed_CPA_MANAGER_DATA_KEY_PATH}"
      fi
      if [ -z "${CPA_MANAGER_PLUS_RUN_DIR+x}" ] && [ -n "${parsed_CPA_MANAGER_PLUS_RUN_DIR}" ]; then
        export CPA_MANAGER_PLUS_RUN_DIR="${parsed_CPA_MANAGER_PLUS_RUN_DIR}"
      fi
      if [ -z "${CPA_MANAGER_PLUS_LOG_DIR+x}" ] && [ -n "${parsed_CPA_MANAGER_PLUS_LOG_DIR}" ]; then
        export CPA_MANAGER_PLUS_LOG_DIR="${parsed_CPA_MANAGER_PLUS_LOG_DIR}"
      fi
      if [ -z "${CPAMP_RUNTIME_TOKEN_FILE+x}" ] && [ -n "${parsed_CPAMP_RUNTIME_TOKEN_FILE}" ]; then
        export CPAMP_RUNTIME_TOKEN_FILE="${parsed_CPAMP_RUNTIME_TOKEN_FILE}"
      fi
      if [ -z "${CPAMP_CPA_EXECUTABLE+x}" ] && [ -n "${parsed_CPAMP_CPA_EXECUTABLE}" ]; then
        export CPAMP_CPA_EXECUTABLE="${parsed_CPAMP_CPA_EXECUTABLE}"
      fi
      if [ -z "${CPAMP_RUNTIME_ADDR+x}" ] && [ -n "${parsed_CPAMP_RUNTIME_ADDR}" ]; then
        export CPAMP_RUNTIME_ADDR="${parsed_CPAMP_RUNTIME_ADDR}"
      fi
      if [ -z "${CPAMP_MANAGER_ADDR+x}" ] && [ -n "${parsed_CPAMP_MANAGER_ADDR}" ]; then
        export CPAMP_MANAGER_ADDR="${parsed_CPAMP_MANAGER_ADDR}"
      fi
      if [ -z "${CPAMP_INGRESS_CONTROL_ADDR+x}" ] && [ -n "${parsed_CPAMP_INGRESS_CONTROL_ADDR}" ]; then
        export CPAMP_INGRESS_CONTROL_ADDR="${parsed_CPAMP_INGRESS_CONTROL_ADDR}"
      fi
      if [ -z "${CPAMP_RUNTIME_CPA_ADDR+x}" ] && [ -z "${CPAMP_CPA_PORT+x}" ] &&
        [ -z "${CPAMP_GATEWAY_URL+x}" ] && [ -z "${CPA_UPSTREAM_URL+x}" ]; then
        if [ -n "${parsed_CPAMP_RUNTIME_CPA_ADDR}" ]; then
          export CPAMP_RUNTIME_CPA_ADDR="${parsed_CPAMP_RUNTIME_CPA_ADDR}"
        elif [ -n "${parsed_CPAMP_CPA_PORT}" ]; then
          export CPAMP_CPA_PORT="${parsed_CPAMP_CPA_PORT}"
        elif [ -n "${parsed_CPAMP_GATEWAY_URL}" ]; then
          export CPAMP_GATEWAY_URL="${parsed_CPAMP_GATEWAY_URL}"
        elif [ -n "${parsed_CPA_UPSTREAM_URL}" ]; then
          export CPA_UPSTREAM_URL="${parsed_CPA_UPSTREAM_URL}"
        fi
      fi
      if [ -z "${CPAMP_DRAIN_TIMEOUT+x}" ] && [ -n "${parsed_CPAMP_DRAIN_TIMEOUT}" ]; then
        export CPAMP_DRAIN_TIMEOUT="${parsed_CPAMP_DRAIN_TIMEOUT}"
      fi
      if [ -z "${CPAMP_RUNTIME_CPA_UID+x}" ] && [ -z "${CPAMP_RUNTIME_CPA_GID+x}" ] &&
        [ -n "${parsed_CPAMP_RUNTIME_CPA_UID}" ] && [ -n "${parsed_CPAMP_RUNTIME_CPA_GID}" ]; then
        export CPAMP_RUNTIME_CPA_UID="${parsed_CPAMP_RUNTIME_CPA_UID}"
        export CPAMP_RUNTIME_CPA_GID="${parsed_CPAMP_RUNTIME_CPA_GID}"
      fi
      if [ -z "${CPAMP_CPA_USER+x}" ] && [ -n "${parsed_CPAMP_CPA_USER}" ]; then
        export CPAMP_CPA_USER="${parsed_CPAMP_CPA_USER}"
      fi
      if [ -z "${CPAMP_RUNTIME_DATA_DIR+x}" ] && [ -n "${parsed_CPAMP_RUNTIME_DATA_DIR}" ]; then
        export CPAMP_RUNTIME_DATA_DIR="${parsed_CPAMP_RUNTIME_DATA_DIR}"
      fi
      if [ -z "${CPAMP_RUNTIME_JOURNAL_PATH+x}" ] && [ -n "${parsed_CPAMP_RUNTIME_JOURNAL_PATH}" ]; then
        export CPAMP_RUNTIME_JOURNAL_PATH="${parsed_CPAMP_RUNTIME_JOURNAL_PATH}"
      fi
      if [ -z "${CPA_MANAGER_PLUS_LAUNCHER+x}" ] && [ -n "${parsed_CPA_MANAGER_PLUS_LAUNCHER}" ]; then
        export CPA_MANAGER_PLUS_LAUNCHER="${parsed_CPA_MANAGER_PLUS_LAUNCHER}"
      fi
      exec "$@"
    )
  }

  local upgrade_lock_dir="${target_home}/.upgrade.lock"
  acquire_upgrade_lock "${upgrade_lock_dir}"
  _cpamp_swap_incomplete=0
  local _cpamp_in_signal_handler=0
  _cpamp_handle_upgrade_signal() {
    local sig="$1"
    if [ "${_cpamp_in_signal_handler}" -eq 1 ]; then
      return 0
    fi
    _cpamp_in_signal_handler=1
    echo "Upgrade interrupted by SIG${sig}" >&2
    if [ "${_cpamp_swap_incomplete:-0}" -eq 1 ]; then
      rollback_and_fail "Upgrade swap interrupted by SIG${sig}; rolling back"
    fi
    exit 1
  }
  trap '_cpamp_handle_upgrade_signal INT' INT
  trap '_cpamp_handle_upgrade_signal TERM' TERM

  local check_dir sym
  for check_dir in "${source_dir}/bin" "${source_dir}/share"; do
    if [ -L "${check_dir}" ] || [ ! -d "${check_dir}" ]; then
      echo "Invalid release bundle: missing non-symlink directory ${check_dir}" >&2
      exit 1
    fi
    sym="$(find "${check_dir}" -type l -print 2>/dev/null | sed -n '1p')"
    if [ -n "${sym}" ]; then
      echo "Invalid release bundle: refusing symlink ${sym}" >&2
      exit 1
    fi
  done
  for check_dir in "${source_dir}/licenses" "${source_dir}/docs"; do
    [ -e "${check_dir}" ] || [ -L "${check_dir}" ] || continue
    if [ -L "${check_dir}" ] || [ ! -d "${check_dir}" ]; then
      echo "Invalid release bundle: ${check_dir} must be a non-symlink directory" >&2
      exit 1
    fi
    sym="$(find "${check_dir}" -type l -print 2>/dev/null | sed -n '1p')"
    if [ -n "${sym}" ]; then
      echo "Invalid release bundle: refusing symlink ${sym}" >&2
      exit 1
    fi
  done

  local req_file req_path
  for req_file in \
    bin/cpa-manager-plus \
    bin/cpa-manager-plus-ingress \
    bin/cpa-manager-plus-runtime-supervisor \
    bin/cli-proxy-api \
    bin/cpa-manager-plus-launcher \
    cpa-manager-plus-launcher \
    cpa-manager-plusctl \
    cpa-manager-plus.service \
    share/cpamp/provenance.json \
    share/cpamp/runtime/seed-lineage.json; do
    req_path="${source_dir}/${req_file}"
    if [ -L "${req_path}" ] || [ ! -f "${req_path}" ]; then
      echo "Invalid release bundle: missing regular non-symlink file ${req_file} in ${source_dir}" >&2
      exit 1
    fi
  done

  local stage_dir="${target_home}/.upgrade-stage.$$"
  local backup_dir="${target_home}/.upgrade-backup.$$"
  if [ -e "${stage_dir}" ] || [ -L "${stage_dir}" ] || [ -e "${backup_dir}" ] || [ -L "${backup_dir}" ]; then
    echo "Upgrade temporary directory already exists under ${target_home}; clear stale .upgrade-* paths and retry" >&2
    exit 1
  fi

  _cpamp_upgrade_stage_dir="${stage_dir}"
  (umask 022 && mkdir -m 0755 "${stage_dir}")

  local release_items=(
    bin
    share
    licenses
    docs
    cpa-manager-plus-launcher
    cpa-manager-plusctl
    cpa-manager-plus.service
    README.md
    README_CN.md
    LICENSE
    THIRD_PARTY_NOTICES.txt
  )
  local copied_items=()
  local item
  for item in "${release_items[@]}"; do
    if [ -L "${source_dir}/${item}" ]; then
      echo "Invalid release bundle: refusing symlink ${source_dir}/${item}" >&2
      exit 1
    fi
    if [ -e "${source_dir}/${item}" ]; then
      cp -R "${source_dir}/${item}" "${stage_dir}/${item}"
      copied_items+=("${item}")
    fi
  done

  chmod -R go-w "${stage_dir}"
  find "${stage_dir}" -type d -exec chmod 0755 {} +
  find "${stage_dir}" -type f -exec chmod 0644 {} +
  find "${stage_dir}/bin" -type f -exec chmod 0755 {} +
  chmod 0755 "${stage_dir}/cpa-manager-plus-launcher" "${stage_dir}/cpa-manager-plusctl"
  if [ "$(id -u)" -eq 0 ]; then
    chown -R -h 0:0 "${stage_dir}"
  fi

  verify_staged_release_dir "${stage_dir}"

  local systemctl_bin="${CPA_MANAGER_PLUS_SYSTEMCTL_BIN:-}"
  if [ -z "${systemctl_bin}" ] && command -v systemctl >/dev/null 2>&1 && [ "$(id -u)" -eq 0 ]; then
    systemctl_bin="systemctl"
  fi

  local pid_state cpa_pids
  pid_state="$(inspect_instance_pid "${eff_pid_file}")"
  if [ "${pid_state}" = "conflict" ]; then
    echo "Refusing upgrade: ${eff_pid_file} points to a running process that could not be strongly verified." >&2
    exit 1
  fi

  if [ "${skip_systemctl}" != "1" ] && [ -n "${systemctl_bin}" ]; then
    inspect_systemd_unit_state "${systemctl_bin}" "${service_name}"
  fi

  enter_upgrade_swap_phase "${upgrade_lock_dir}"

  local was_systemd_active=0
  local was_ctl_active=0
  if [ "${skip_systemctl}" != "1" ] && [ -n "${systemctl_bin}" ] &&
    [ "${_systemd_unit_state}" = "busy" ]; then
    was_systemd_active=1
    "${systemctl_bin}" stop "${service_name}"
  fi

  pid_state="$(inspect_instance_pid "${eff_pid_file}")"
  if [ "${pid_state}" = "conflict" ]; then
    echo "Refusing upgrade: ${eff_pid_file} points to a running process that could not be strongly verified." >&2
    exit 1
  fi
  cpa_pids="$(
    if [ -n "${eff_runtime_data_dir}" ]; then export CPAMP_RUNTIME_DATA_DIR="${eff_runtime_data_dir}"; fi
    if [ -n "${eff_journal_path}" ]; then export CPAMP_RUNTIME_JOURNAL_PATH="${eff_journal_path}"; fi
    if [ -n "${eff_cpa_user}" ]; then export CPAMP_CPA_USER="${eff_cpa_user}"; fi
    find_attributable_cpa_pids_for_instance "${eff_run_dir}" "${eff_data_dir}" "${eff_cpa_bin}" "${eff_cpa_addr}" "${eff_cpa_uid}"
  )"
  if [ "${pid_state}" = "running" ] ||
    child_pid_if_active "${eff_run_dir}/ingress.pid" >/dev/null 2>&1 ||
    child_pid_if_active "${eff_run_dir}/manager.pid" >/dev/null 2>&1 ||
    child_pid_if_active "${eff_run_dir}/runtime-supervisor.pid" >/dev/null 2>&1 ||
    [ -n "${cpa_pids}" ]; then
    if [ "${was_systemd_active}" -eq 0 ]; then
      was_ctl_active=1
    fi
    local stop_ctl_bin="${target_home}/cpa-manager-plusctl"
    if [ ! -x "${stop_ctl_bin}" ] || [ -L "${stop_ctl_bin}" ]; then
      stop_ctl_bin="${source_dir}/cpa-manager-plusctl"
    fi
    CPAMP_UPGRADE_LOCK_TOKEN="${_cpamp_upgrade_lock_token}" run_with_target_unit_env "${stop_ctl_bin}" stop
  fi

  # Hard quiescence verification before moving any file in target_home.
  if [ "${skip_systemctl}" != "1" ] && [ -n "${systemctl_bin}" ]; then
    inspect_systemd_unit_state "${systemctl_bin}" "${service_name}"
    if [ "${_systemd_unit_state}" = "busy" ]; then
      echo "Refusing upgrade: systemd unit ${service_name} is still active or transitioning after stop (${_systemd_load_state}/${_systemd_active_state}/${_systemd_sub_state})" >&2
      exit 1
    fi
  fi
  pid_state="$(inspect_instance_pid "${eff_pid_file}")"
  if [ "${pid_state}" != "absent" ] && [ "${pid_state}" != "stale" ]; then
    echo "Refusing upgrade: launcher PID state at ${eff_pid_file} is ${pid_state} (expected absent or stale)" >&2
    exit 1
  fi
  local q_child q_pid
  for q_child in ingress manager runtime-supervisor; do
    if q_pid="$(child_pid_if_active "${eff_run_dir}/${q_child}.pid" 2>/dev/null)"; then
      echo "Refusing upgrade: ${q_child} is still running (PID ${q_pid}) after stop" >&2
      exit 1
    fi
  done
  cpa_pids="$(
    if [ -n "${eff_runtime_data_dir}" ]; then export CPAMP_RUNTIME_DATA_DIR="${eff_runtime_data_dir}"; fi
    if [ -n "${eff_journal_path}" ]; then export CPAMP_RUNTIME_JOURNAL_PATH="${eff_journal_path}"; fi
    if [ -n "${eff_cpa_user}" ]; then export CPAMP_CPA_USER="${eff_cpa_user}"; fi
    find_attributable_cpa_pids_for_instance "${eff_run_dir}" "${eff_data_dir}" "${eff_cpa_bin}" "${eff_cpa_addr}" "${eff_cpa_uid}"
  )"
  if [ -n "${cpa_pids}" ]; then
    echo "Refusing upgrade: attributable CPA child process(es) still running (${cpa_pids}) after stop" >&2
    exit 1
  fi

  local moved_to_backup=()
  local moved_into_target=()
  local had_existing_unit=0
  local unit_install_attempted=0
  local rollback_ok=1
  local eff_runtime_base="${eff_runtime_data_dir:-"${eff_data_dir}/runtime"}"
  local eff_journal_resolved="${eff_journal_path:-"${eff_runtime_base}/supervisor/operations.sqlite"}"
  local eff_runtime_root
  eff_runtime_root="$(dirname "$(dirname "${eff_journal_resolved}")")"
  local eff_slot_state_file="${eff_runtime_root}/cpamp/slot-state.json"
  local eff_slots_dir="${eff_runtime_root}/cpamp/slots"
  local slot_state_reset_performed=0
  local new_bundle_version=""
  rollback_upgrade_swap() {
    set +e
    rollback_ok=1
    local r_item
    local removed_targets=" "
    if [ "${#moved_into_target[@]}" -gt 0 ]; then
      for r_item in "${moved_into_target[@]}"; do
        removed_targets="${removed_targets}${r_item} "
        if [ -e "${target_home}/${r_item}" ] || [ -L "${target_home}/${r_item}" ]; then
          if ! rm -rf "${target_home}/${r_item}"; then
            echo "Rollback error: failed to remove ${target_home}/${r_item}" >&2
            rollback_ok=0
          fi
        fi
      done
    fi
    if [ "${#copied_items[@]}" -gt 0 ]; then
      for r_item in "${copied_items[@]}"; do
        case "${removed_targets}" in
          *" ${r_item} "*) continue ;;
        esac
        if [ ! -e "${stage_dir}/${r_item}" ] && [ ! -L "${stage_dir}/${r_item}" ]; then
          removed_targets="${removed_targets}${r_item} "
          if [ -e "${target_home}/${r_item}" ] || [ -L "${target_home}/${r_item}" ]; then
            if ! rm -rf "${target_home}/${r_item}"; then
              echo "Rollback error: failed to remove ${target_home}/${r_item}" >&2
              rollback_ok=0
            fi
          fi
        fi
      done
    fi
    local restored_backups=" "
    if [ "${#moved_to_backup[@]}" -gt 0 ]; then
      for r_item in "${moved_to_backup[@]}"; do
        restored_backups="${restored_backups}${r_item} "
        if [ -e "${backup_dir}/${r_item}" ] || [ -L "${backup_dir}/${r_item}" ]; then
          if [ -e "${target_home}/${r_item}" ] || [ -L "${target_home}/${r_item}" ]; then
            rm -rf "${target_home}/${r_item}" 2>/dev/null || true
          fi
          if ! mv "${backup_dir}/${r_item}" "${target_home}/${r_item}"; then
            echo "Rollback error: failed to restore ${target_home}/${r_item}" >&2
            rollback_ok=0
          fi
        else
          echo "Rollback error: missing backup entry ${backup_dir}/${r_item}" >&2
          rollback_ok=0
        fi
      done
    fi
    for r_item in "${release_items[@]}"; do
      case "${restored_backups}" in
        *" ${r_item} "*) continue ;;
      esac
      if [ -e "${backup_dir}/${r_item}" ] || [ -L "${backup_dir}/${r_item}" ]; then
        if [ -e "${target_home}/${r_item}" ] || [ -L "${target_home}/${r_item}" ]; then
          rm -rf "${target_home}/${r_item}" 2>/dev/null || true
        fi
        if ! mv "${backup_dir}/${r_item}" "${target_home}/${r_item}"; then
          echo "Rollback error: failed to restore ${target_home}/${r_item}" >&2
          rollback_ok=0
        fi
      fi
    done
    if [ "${unit_install_attempted}" -eq 1 ]; then
      if [ "${had_existing_unit}" -eq 1 ]; then
        if [ -f "${backup_dir}/unit.service" ]; then
          if ! cp -p "${backup_dir}/unit.service" "${unit_path}.rollback.$$" ||
            ! mv -f "${unit_path}.rollback.$$" "${unit_path}"; then
            rm -f "${unit_path}.rollback.$$" 2>/dev/null || true
            echo "Rollback error: failed to restore ${unit_path}" >&2
            rollback_ok=0
          fi
        else
          echo "Rollback error: missing unit backup ${backup_dir}/unit.service" >&2
          rollback_ok=0
        fi
      else
        if [ -f "${unit_path}" ] || [ -L "${unit_path}" ]; then
          if ! rm -f "${unit_path}"; then
            echo "Rollback error: failed to remove ${unit_path}" >&2
            rollback_ok=0
          fi
        fi
      fi
      if [ "${skip_systemctl}" != "1" ] && [ -n "${systemctl_bin}" ]; then
        if ! "${systemctl_bin}" daemon-reload >/dev/null 2>&1; then
          echo "Rollback error: systemctl daemon-reload failed after restoring ${unit_path}" >&2
          rollback_ok=0
        fi
      fi
    fi
    if [ "${slot_state_reset_performed}" -eq 1 ] && [ -f "${backup_dir}/slot-state.json.bak" ] && [ ! -L "${eff_slot_state_file}" ]; then
      if ! cp -p "${backup_dir}/slot-state.json.bak" "${eff_slot_state_file}.rollback.$$" ||
        ! mv -f "${eff_slot_state_file}.rollback.$$" "${eff_slot_state_file}"; then
        rm -f "${eff_slot_state_file}.rollback.$$" 2>/dev/null || true
        echo "Rollback error: failed to restore ${eff_slot_state_file}" >&2
        rollback_ok=0
      fi
    fi
    if [ "${rollback_ok}" -eq 1 ]; then
      rm -rf "${backup_dir}" 2>/dev/null || true
    else
      echo "CRITICAL: Rollback encountered errors; backup preserved at ${backup_dir}" >&2
    fi
  }

  rollback_and_fail() {
    local reason="$1"
    local restore_restart_failed=0
    _cpamp_in_signal_handler=1
    echo "${reason}" >&2
    _cpamp_swap_incomplete=1
    enter_upgrade_swap_phase "${upgrade_lock_dir}"
    rollback_upgrade_swap
    if [ "${rollback_ok}" -eq 1 ]; then
      _cpamp_swap_incomplete=0
      leave_upgrade_swap_phase "${upgrade_lock_dir}"
      trap - INT TERM
      if [ "${was_systemd_active}" -eq 1 ] && [ "${skip_systemctl}" != "1" ] && [ -n "${systemctl_bin}" ]; then
        set +e
        rm -f "${eff_run_dir}/launcher.ready"
        if ! "${systemctl_bin}" start "${service_name}" ||
          ! wait_for_systemd_unit_ready "${systemctl_bin}" "${service_name}" "${eff_run_dir}"; then
          restore_restart_failed=1
        fi
        set -e
      elif [ "${was_ctl_active}" -eq 1 ]; then
        set +e
        if ! CPAMP_UPGRADE_LOCK_TOKEN="${_cpamp_upgrade_lock_token}" run_with_target_unit_env "${target_home}/cpa-manager-plusctl" start ||
          ! run_with_target_unit_env "${target_home}/cpa-manager-plusctl" status >/dev/null 2>&1; then
          restore_restart_failed=1
        fi
        set -e
      fi
      if [ "${restore_restart_failed}" -eq 1 ]; then
        echo "Warning: rollback restored files cleanly, but failed to restart previously active instance" >&2
      fi
    fi
    exit 1
  }

  (umask 077 && mkdir -m 0700 "${backup_dir}")
  if [ -f "${unit_path}" ]; then
    if ! cp -p "${unit_path}" "${backup_dir}/unit.service"; then
      echo "Upgrade failed while backing up ${unit_path}; aborting before swap" >&2
      rm -rf "${backup_dir}" 2>/dev/null || true
      exit 1
    fi
    had_existing_unit=1
  fi

  _cpamp_swap_incomplete=1

  for item in "${release_items[@]}"; do
    if [ -e "${target_home}/${item}" ] || [ -L "${target_home}/${item}" ]; then
      if ! mv "${target_home}/${item}" "${backup_dir}/${item}"; then
        rollback_and_fail "Upgrade failed while backing up ${item}; rolling back"
      fi
      moved_to_backup+=("${item}")
    fi
  done

  for item in "${copied_items[@]}"; do
    if ! mv "${stage_dir}/${item}" "${target_home}/${item}"; then
      rollback_and_fail "Upgrade failed while installing ${item}; rolling back"
    fi
    moved_into_target+=("${item}")
  done

  new_bundle_version="$(_extract_json_string_field "${target_home}/share/cpamp/provenance.json" "version")"
  if [ -n "${new_bundle_version}" ] && [ ! -L "${eff_slot_state_file}" ] && [ -f "${eff_slot_state_file}" ]; then
    local prev_active_slot=""
    prev_active_slot="$(_extract_json_string_field "${eff_slot_state_file}" "active_slot")"
    if [ -n "${prev_active_slot}" ] && semver_v2_gte "${new_bundle_version}" "${prev_active_slot}"; then
      if cp -p "${eff_slot_state_file}" "${backup_dir}/slot-state.json.bak" 2>/dev/null; then
        local now_utc=""
        now_utc="$(date -u '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || printf '1970-01-01T00:00:00Z')"
        local reset_tmp="${eff_slot_state_file}.tmp.$$"
        (
          umask 077
          cat >"${reset_tmp}" <<EOF
{
  "schema_version": 1,
  "state": "none",
  "active_slot": "",
  "previous_slot": "",
  "base_version_at_switch": "",
  "trial_id": "",
  "snapshot_dir": "",
  "schema_generation": 1,
  "previous_schema_generation": 1,
  "last_failure_code": "",
  "last_rolled_back_slot": "",
  "updated_at": "${now_utc}"
}
EOF
        )
        chmod 0600 "${reset_tmp}" 2>/dev/null || true
        if [ "$(id -u)" -eq 0 ]; then
          chown -h 0:0 "${reset_tmp}" 2>/dev/null || true
        fi
        if mv -f "${reset_tmp}" "${eff_slot_state_file}"; then
          slot_state_reset_performed=1
        else
          rm -f "${reset_tmp}" 2>/dev/null || true
        fi
      fi
    fi
  fi
  local install_args=(install --home "${target_home}" --service-name "${service_name}" --unit-dir "${unit_dir}")
  if [ "${skip_systemctl}" = "1" ]; then
    install_args+=(--no-systemctl)
  fi
  unit_install_attempted=1
  if ! CPAMP_UPGRADE_LOCK_TOKEN="${_cpamp_upgrade_lock_token}" \
    CPAMP_PRESERVED_ENV_FILES="${preserved_env_files%$'\n'}" \
    CPAMP_PRESERVED_RW_PATHS="${preserved_rw_paths%$'\n'}" \
    run_with_target_unit_env "${target_home}/cpa-manager-plusctl" "${install_args[@]}"; then
    rollback_and_fail "Upgrade failed while refreshing service configuration; rolling back"
  fi
  _cpamp_swap_incomplete=0
  if [ "${no_restart}" != "1" ]; then
    leave_upgrade_swap_phase "${upgrade_lock_dir}"
    local start_mode="none" start_ok=1 start_rc=0
    if [ "${was_systemd_active}" -eq 1 ] && [ "${skip_systemctl}" != "1" ] && [ -n "${systemctl_bin}" ]; then
      start_mode="systemd"
    elif [ "${was_ctl_active}" -eq 1 ]; then
      start_mode="ctl"
    elif [ "${skip_systemctl}" != "1" ] && [ -n "${systemctl_bin}" ]; then
      start_mode="systemd"
    fi

    if [ "${start_mode}" = "systemd" ]; then
      set +e
      rm -f "${eff_run_dir}/launcher.ready"
      if "${systemctl_bin}" start "${service_name}"; then
        if wait_for_systemd_unit_ready "${systemctl_bin}" "${service_name}" "${eff_run_dir}"; then
          start_rc=0
        else
          start_rc=$?
        fi
      else
        start_rc=$?
      fi
      set -e
      if [ "${start_rc}" -ne 0 ]; then
        start_ok=0
        "${systemctl_bin}" stop "${service_name}" >/dev/null 2>&1 || true
        local partial_stop_ctl="${target_home}/cpa-manager-plusctl"
        if [ ! -x "${partial_stop_ctl}" ] || [ -L "${partial_stop_ctl}" ]; then
          partial_stop_ctl="${source_dir}/cpa-manager-plusctl"
        fi
        run_with_target_unit_env "${partial_stop_ctl}" stop >/dev/null 2>&1 || true
      fi
    elif [ "${start_mode}" = "ctl" ]; then
      set +e
      CPAMP_UPGRADE_LOCK_TOKEN="${_cpamp_upgrade_lock_token}" \
        run_with_target_unit_env "${target_home}/cpa-manager-plusctl" start
      start_rc=$?
      if [ "${start_rc}" -eq 0 ]; then
        run_with_target_unit_env "${target_home}/cpa-manager-plusctl" status >/dev/null 2>&1
        start_rc=$?
      fi
      set -e
      if [ "${start_rc}" -ne 0 ]; then
        start_ok=0
        local partial_stop_ctl="${target_home}/cpa-manager-plusctl"
        if [ ! -x "${partial_stop_ctl}" ] || [ -L "${partial_stop_ctl}" ]; then
          partial_stop_ctl="${source_dir}/cpa-manager-plusctl"
        fi
        run_with_target_unit_env "${partial_stop_ctl}" stop >/dev/null 2>&1 || true
      fi
    else
      echo "Upgrade complete."
    fi

    if [ "${start_ok}" -ne 1 ]; then
      rollback_and_fail "Upgrade failed during post-upgrade start/readiness check; rolling back"
    fi
  else
    leave_upgrade_swap_phase "${upgrade_lock_dir}"
    echo "Upgrade complete (--no-restart)."
  fi

  if [ "${slot_state_reset_performed}" -eq 1 ] && [ -n "${new_bundle_version}" ] && [ ! -L "${eff_slots_dir}" ] && [ -d "${eff_slots_dir}" ]; then
    local slot_dir_candidate slot_ver_name
    for slot_dir_candidate in "${eff_slots_dir}"/v*; do
      [ -e "${slot_dir_candidate}" ] || continue
      [ -L "${slot_dir_candidate}" ] && continue
      [ -d "${slot_dir_candidate}" ] || continue
      slot_ver_name="$(basename "${slot_dir_candidate}")"
      if semver_v2_gte "${new_bundle_version}" "${slot_ver_name}"; then
        chmod -R u+w "${slot_dir_candidate}" 2>/dev/null || true
        rm -rf "${slot_dir_candidate}" 2>/dev/null || true
      fi
    done
  fi
  rm -rf "${backup_dir}" "${stage_dir}"
  _cpamp_upgrade_stage_dir=""
  _cpamp_cleanup_upgrade_dirs
  trap - INT TERM EXIT
}

command="${1:-status}"
if [ "$#" -gt 0 ]; then
  shift
fi

case "${command}" in
  install)
    install_service "$@"
    ;;
  upgrade)
    upgrade_app "$@"
    ;;
  start)
    start_app "$@"
    ;;
  stop)
    stop_app
    ;;
  restart)
    restart_app "$@"
    ;;
  status)
    status_app
    ;;
  logs)
    show_logs "$@"
    ;;
  help | -h | --help)
    usage
    ;;
  *)
    usage >&2
    exit 1
    ;;
esac
