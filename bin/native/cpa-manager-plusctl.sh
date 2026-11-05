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
    [ -f "${run_dir}/ingress.pid" ]; then
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

  if [ ! -f "${target_pid_file}" ]; then
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
  if [ "$(id -u)" -eq 0 ] && [ "$(uname -s)" = "Linux" ] && id -u "${cpa_user}" >/dev/null 2>&1; then
    id -u "${cpa_user}"
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
cpa_runtime_root() {
  local runtime_data_dir="${CPAMP_RUNTIME_DATA_DIR:-"${data_dir}/runtime"}"
  local journal_path="${CPAMP_RUNTIME_JOURNAL_PATH:-"${runtime_data_dir}/supervisor/operations.sqlite"}"

  dirname "$(dirname "${journal_path}")"
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
  if [ -n "${exe}" ] && [ "${exe}" = "${cpa_bin}" ]; then
    entry_ok=1
  fi

  previous=""
  while IFS= read -r token; do
    if [ "${entry_ok}" -eq 0 ] && [ "${token}" = "${cpa_bin}" ]; then
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
  local recorded supervisor_pid

  cpa_process_matches_instance "${pid}" "${runtime_root}" || return 1

  recorded="$(cpa_provenance_record_start "${provenance_file}" "${pid}" || true)"
  if [ -n "${recorded}" ]; then
    [ "${recorded}" = "$(process_start_marker "${pid}")" ]
    return $?
  fi

  supervisor_pid="$(child_pid_if_active "${run_dir}/runtime-supervisor.pid" 2>/dev/null || true)"
  process_is_descendant_of "${pid}" "${supervisor_pid}"
}

reap_orphaned_cpa_processes() {
  local address runtime_root provenance_file
  local port candidates pid pids="" i alive

  address="$(resolve_cpa_address)"
  runtime_root="$(cpa_runtime_root)"
  provenance_file="$(cpa_provenance_file)"

  port="$(printf '%s\n' "${address}" | sed -n 's/.*://p')"
  case "${port}" in
    '' | *[!0-9]*) return 0 ;;
  esac

  candidates="$(
    listening_pids_on_port "${port}"
    # The entry point travels through the environment: as an awk operand it
    # would match the command line of the collector itself.
    ps -eo pid=,args= 2>/dev/null |
      CPAMP_CPA_ENTRY_POINT="${cpa_bin}" awk '
        index($0, ENVIRON["CPAMP_CPA_ENTRY_POINT"]) == 0 { next }
        { print $1 }
      '
  )"

  for pid in $(printf '%s\n' "${candidates}" | sed '/^[[:space:]]*$/d' | sort -u); do
    [ "${pid}" = "$$" ] && continue
    is_pid_running "${pid}" || continue
    if ! cpa_process_is_instance_owned "${pid}" "${runtime_root}" "${provenance_file}"; then
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
      if ! cpa_process_is_instance_owned "${pid}" "${runtime_root}" "${provenance_file}"; then
        echo "Refusing to SIGKILL PID ${pid} on ${address}: its instance provenance no longer matches" >&2
        continue
      fi
      echo "Orphaned CPA child (PID ${pid}) did not exit; sending SIGKILL" >&2
      kill -KILL "${pid}" >/dev/null 2>&1 || true
    fi
  done
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
  rm -f "${run_dir}/launcher.ready"
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
      launcher_cpa_address="$(resolve_cpa_address_for_start)"
    fi
    export CPAMP_RUNTIME_CPA_ADDR="${launcher_cpa_address}"
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

  kill "${pid}"
  if is_v2_mode; then
    local stop_timeout="${CPAMP_STOP_TIMEOUT_SEC:-15}"
    local max_ticks=$((stop_timeout * 10))
    local tick=0
    while [ "${tick}" -lt "${max_ticks}" ]; do
      if ! recorded_instance_running &&
        ! child_pid_if_active "${run_dir}/ingress.pid" >/dev/null 2>&1 &&
        ! child_pid_if_active "${run_dir}/manager.pid" >/dev/null 2>&1 &&
        ! child_pid_if_active "${run_dir}/runtime-supervisor.pid" >/dev/null 2>&1; then
        rm -f "${pid_file}" "${run_dir}/launcher.ready" \
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
  local persisted="${run_dir}/cpa.address"
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

# `start` honours an explicit override first, then the address this installation
# last ran with, then the default. The resolved value is exported to the
# launcher, so a restart can never drift back to the hardcoded 8317.
resolve_cpa_address_for_start() {
  if [ -n "${CPAMP_RUNTIME_CPA_ADDR:-}" ] || [ -n "${CPAMP_CPA_PORT:-}" ] ||
    [ -n "${CPAMP_GATEWAY_URL:-}" ] || [ -n "${CPA_UPSTREAM_URL:-}" ]; then
    cpa_address_from_environment
    return 0
  fi
  read_persisted_cpa_address || printf '%s\n' "127.0.0.1:8317"
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

  local target_bin_dir="${CPA_MANAGER_PLUS_BIN_DIR:-"${target_home}/bin"}"
  local target_runtime_bin="${CPA_MANAGER_PLUS_RUNTIME_SUPERVISOR_BIN:-"${target_bin_dir}/cpa-manager-plus-runtime-supervisor"}"
  local target_manager_bin="${CPA_MANAGER_PLUS_MANAGER_BIN:-"${CPA_MANAGER_PLUS_BIN:-"${target_bin_dir}/cpa-manager-plus"}"}"
  local target_ingress_bin="${CPA_MANAGER_PLUS_INGRESS_BIN:-"${target_bin_dir}/cpa-manager-plus-ingress"}"
  local target_cpa_bin="${CPAMP_CPA_EXECUTABLE:-"${target_bin_dir}/cli-proxy-api"}"
  local target_launcher_bin="${CPA_MANAGER_PLUS_LAUNCHER:-"${target_bin_dir}/cpa-manager-plus-launcher"}"

  if [ ! -x "${target_launcher_bin}" ]; then
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
    if [ ! -f "${comp_bin}" ] || [ ! -x "${comp_bin}" ]; then
      echo "Missing or non-executable ${comp_name} binary for install: ${comp_bin}" >&2
      exit 1
    fi
  done

  local target_data_dir="${USAGE_DATA_DIR:-"${CPA_MANAGER_PLUS_DATA_DIR:-"${target_home}/data"}"}"
  local target_run_dir="${CPA_MANAGER_PLUS_RUN_DIR:-"${target_home}/run"}"
  local target_log_dir="${CPA_MANAGER_PLUS_LOG_DIR:-"${target_home}/logs"}"
  local target_token_file="${CPAMP_RUNTIME_TOKEN_FILE:-"${target_run_dir}/runtime-token"}"
  local target_db_path="${USAGE_DB_PATH:-"${target_data_dir}/usage.sqlite"}"
  local target_data_key_path="${CPA_MANAGER_DATA_KEY_PATH:-"${target_data_dir}/data.key"}"

  ensure_private_dir "${target_data_dir}" "${manage_data_dir}"
  ensure_private_dir "${target_run_dir}" "${manage_run_dir}"
  ensure_private_dir "${target_log_dir}" "${manage_log_dir}"

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

  mkdir -p "${unit_dir}"
  local unit_path="${unit_dir}/${service_name}"
  local public_http_addr="${HTTP_ADDR:-"${CPAMP_INGRESS_ADDR:-0.0.0.0:18317}"}"
  local runtime_addr="${CPAMP_RUNTIME_ADDR:-127.0.0.1:18318}"
  local manager_addr="${CPAMP_MANAGER_ADDR:-127.0.0.1:18319}"
  local ingress_control_addr="${CPAMP_INGRESS_CONTROL_ADDR:-127.0.0.1:18320}"
  local cpa_addr
  cpa_addr="$(resolve_cpa_address)"

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
Environment="CPAMP_RUNTIME_CPA_UID=${cpa_uid}"
Environment="CPAMP_RUNTIME_CPA_GID=${cpa_gid}"
EnvironmentFile=-${target_home}/cpa-manager-plus.env
ExecStart=${target_launcher_bin}
KillMode=mixed
KillSignal=SIGTERM
TimeoutStopSec=30
Restart=on-failure
RestartSec=5
UMask=0077
ProtectSystem=strict
ReadWritePaths=${target_data_dir} ${target_run_dir} ${target_log_dir}
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
  } >"${unit_path}"
  chmod 0644 "${unit_path}"

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

command="${1:-status}"
if [ "$#" -gt 0 ]; then
  shift
fi

case "${command}" in
  install)
    install_service "$@"
    ;;
  start)
    start_app "$@"
    ;;
  stop)
    stop_app
    ;;
  restart)
    stop_app
    start_app "$@"
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
