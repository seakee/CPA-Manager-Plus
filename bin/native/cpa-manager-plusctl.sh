#!/usr/bin/env bash
set -euo pipefail
umask 077

app_name="cpa-manager-plus"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
default_run_dir="${script_dir}/run"
default_log_dir="${script_dir}/logs"
binary="${CPA_MANAGER_PLUS_BIN:-"${script_dir}/${app_name}"}"
run_dir="${CPA_MANAGER_PLUS_RUN_DIR:-"${default_run_dir}"}"
log_dir="${CPA_MANAGER_PLUS_LOG_DIR:-"${default_log_dir}"}"
pid_file="${CPA_MANAGER_PLUS_PID_FILE:-"${run_dir}/${app_name}.pid"}"
log_file="${CPA_MANAGER_PLUS_LOG_FILE:-"${log_dir}/${app_name}.log"}"
manage_run_dir="false"
manage_log_dir="false"

if [ -z "${CPA_MANAGER_PLUS_RUN_DIR:-}" ]; then
  manage_run_dir="true"
fi
if [ -z "${CPA_MANAGER_PLUS_LOG_DIR:-}" ]; then
  manage_log_dir="true"
fi

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
  start [args...]  Start cpa-manager-plus in the background
  stop             Stop the background process
  restart          Restart the background process
  status           Show process status
  logs [lines|-f|--follow]
                   Print recent logs, or follow with -f/--follow

Environment overrides:
  CPA_MANAGER_PLUS_BIN       Binary path
  CPA_MANAGER_PLUS_RUN_DIR   Runtime directory, default: ./run
  CPA_MANAGER_PLUS_LOG_DIR   Log directory, default: ./logs
  CPA_MANAGER_PLUS_PID_FILE  PID file path
  CPA_MANAGER_PLUS_LOG_FILE  Log file path
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

read_pid_record() {
  reset_record

  if [ ! -f "${pid_file}" ]; then
    return 1
  fi

  reject_unsafe_runtime_file "${pid_file}"
  reject_unsafe_runtime_file_parent "${pid_file}"

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
  done <"${pid_file}"

  if ! [[ "${record_pid}" =~ ^[0-9]+$ ]]; then
    return 1
  fi

  if [ "${saw_metadata}" -eq 1 ]; then
    record_format="metadata"
  fi

  return 0
}

is_pid_running() {
  local pid="$1"
  kill -0 "${pid}" >/dev/null 2>&1
}

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

set_pid_record_state() {
  local current_binary current_command current_start

  if [ ! -f "${pid_file}" ]; then
    record_state="missing"
    return
  fi

  if ! read_pid_record; then
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

is_managed_runtime_dir() {
  local dir="$1"

  { [ "${dir}" = "${run_dir}" ] && [ "${manage_run_dir}" = "true" ]; } ||
    { [ "${dir}" = "${log_dir}" ] && [ "${manage_log_dir}" = "true" ]; }
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

start_app() {
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
      echo "${app_name} is not running"
      return 0
      ;;
    stale | invalid)
      rm -f "${pid_file}"
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

command="${1:-status}"
if [ "$#" -gt 0 ]; then
  shift
fi

case "${command}" in
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
