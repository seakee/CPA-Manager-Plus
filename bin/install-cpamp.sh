#!/usr/bin/env bash
set -euo pipefail

repo="seakee/CPA-Manager-Plus"
default_cpamp_image="seakee/cpa-manager-plus:latest"
default_cpa_image="eceasy/cli-proxy-api:latest"
default_install_dir="${HOME:-.}/cpa-manager-plus"

dry_run="${CPAMP_DRY_RUN:-0}"
non_interactive="${CPAMP_NON_INTERACTIVE:-0}"
skip_execute="${CPAMP_SKIP_EXECUTE:-0}"
lang_code="${CPAMP_LANG:-}"
operation="${CPAMP_OPERATION:-}"

os_name="unknown"
arch_name="unknown"
normalized_os="unknown"
normalized_arch="unknown"
is_wsl="false"

install_mode=""
deploy_method=""
install_dir=""
cpamp_port=""
cpa_port=""
cpamp_image=""
cpa_image=""
cpamp_version=""
cpa_connection_mode=""
cpa_url=""
cpa_management_key=""
admin_key=""
demo_client_key=""
generated_admin_key=""
generated_cpa_management_key=""
generated_demo_client_key=""
compose_project_name="${CPAMP_PROJECT_NAME:-cpamp}"
existing_install_state="fresh"
existing_volume_name=""
auth_validation_status="pending"
admin_secret_missing="0"

die() {
  printf '%s\n' "$*" >&2
  exit 1
}

text() {
  case "${lang_code:-zh-CN}:$1" in
    zh-CN:env_header) printf '检测到当前环境' ;;
    en-US:env_header) printf 'Detected environment' ;;
    zh-CN:os) printf '系统' ;;
    en-US:os) printf 'OS' ;;
    zh-CN:arch) printf '架构' ;;
    en-US:arch) printf 'Architecture' ;;
    zh-CN:wsl) printf 'WSL' ;;
    en-US:wsl) printf 'WSL' ;;
    zh-CN:continue) printf '继续使用这个环境安装吗? 输入 yes/no' ;;
    en-US:continue) printf 'Continue with this environment? Enter yes/no' ;;
    zh-CN:select_mode) printf '选择安装范围: 1) CPA + CPAMP 完整安装  2) 仅安装 CPAMP' ;;
    en-US:select_mode) printf 'Select install scope: 1) CPA + CPAMP stack  2) CPAMP only' ;;
    zh-CN:select_method) printf '选择部署方式: 1) Docker  2) 二进制/native' ;;
    en-US:select_method) printf 'Select deployment method: 1) Docker  2) Native binary' ;;
    zh-CN:install_dir) printf '安装目录' ;;
    en-US:install_dir) printf 'Install directory' ;;
    zh-CN:cpamp_port) printf 'CPAMP 对外端口' ;;
    en-US:cpamp_port) printf 'Public CPAMP port' ;;
    zh-CN:cpa_port) printf 'CPA 对外端口' ;;
    en-US:cpa_port) printf 'Public CPA port' ;;
    zh-CN:cpamp_image) printf 'CPAMP Docker 镜像' ;;
    en-US:cpamp_image) printf 'CPAMP Docker image' ;;
    zh-CN:cpa_image) printf 'CPA Docker 镜像' ;;
    en-US:cpa_image) printf 'CPA Docker image' ;;
    zh-CN:version) printf 'CPAMP 版本' ;;
    en-US:version) printf 'CPAMP version' ;;
    zh-CN:cpa_conn_mode) printf 'CPA 连接配置: 1) 现在填写并跳过首次 setup  2) 首次打开面板时填写' ;;
    en-US:cpa_conn_mode) printf 'CPA connection: 1) enter now and skip first setup  2) enter during first setup' ;;
    zh-CN:cpa_url) printf 'CPA 地址' ;;
    en-US:cpa_url) printf 'CPA URL' ;;
    zh-CN:cpa_key) printf 'CPA Management Key' ;;
    en-US:cpa_key) printf 'CPA Management Key' ;;
    zh-CN:summary) printf '安装摘要' ;;
    en-US:summary) printf 'Install summary' ;;
    zh-CN:install_mode_label) printf '安装范围' ;;
    en-US:install_mode_label) printf 'Install scope' ;;
    zh-CN:deploy_method_label) printf '部署方式' ;;
    en-US:deploy_method_label) printf 'Deployment' ;;
    zh-CN:directory_label) printf '安装目录' ;;
    en-US:directory_label) printf 'Directory' ;;
    zh-CN:stack_mode) printf 'CPA + CPAMP 完整安装' ;;
    en-US:stack_mode) printf 'CPA + CPAMP stack' ;;
    zh-CN:cpamp_mode) printf '仅安装 CPAMP' ;;
    en-US:cpamp_mode) printf 'CPAMP only' ;;
    zh-CN:docker_method) printf 'Docker' ;;
    en-US:docker_method) printf 'Docker' ;;
    zh-CN:native_method) printf '二进制/native' ;;
    en-US:native_method) printf 'Native binary' ;;
    zh-CN:cpa_connection_label) printf 'CPA 连接配置' ;;
    en-US:cpa_connection_label) printf 'CPA connection' ;;
    zh-CN:cpa_connection_setup) printf '首次打开面板时填写' ;;
    en-US:cpa_connection_setup) printf 'enter during first setup' ;;
    zh-CN:cpa_connection_env) printf '现在写入本机 secret，跳过首次 setup' ;;
    en-US:cpa_connection_env) printf 'store in local secret now and skip first setup' ;;
    zh-CN:cpa_url_for_cpamp) printf 'CPAMP 使用的 CPA 地址' ;;
    en-US:cpa_url_for_cpamp) printf 'CPA URL for CPAMP' ;;
    zh-CN:confirm) printf '确认执行? 输入 confirm 执行，modify 修改，abort 退出' ;;
    en-US:confirm) printf 'Proceed? Enter confirm to install, modify to change, abort to exit' ;;
    zh-CN:unsupported_stack_native) printf '完整安装包含 CPA 时暂不支持 native。请改选 Docker，或选择仅安装 CPAMP。' ;;
    en-US:unsupported_stack_native) printf 'Native stack install is not supported yet. Choose Docker, or choose CPAMP only.' ;;
    zh-CN:dry_run) printf 'Dry-run：不会写入文件或执行安装命令。' ;;
    en-US:dry_run) printf 'Dry run: no files will be written and no install commands will run.' ;;
    zh-CN:write_file) printf '将写入文件' ;;
    en-US:write_file) printf 'Will write file' ;;
    zh-CN:run_command) printf '将执行命令' ;;
    en-US:run_command) printf 'Will run command' ;;
    zh-CN:done) printf '安装步骤已完成' ;;
    en-US:done) printf 'Install steps completed' ;;
    zh-CN:dry_run_done) printf 'Dry-run 计划预览完成，未写入文件或启动服务' ;;
    en-US:dry_run_done) printf 'Dry-run plan completed; no files were written and no services were started' ;;
    zh-CN:config_done) printf '部署配置已生成，服务尚未启动' ;;
    en-US:config_done) printf 'Deployment config generated; services have not been started' ;;
    zh-CN:operation_skipped) printf '已保留现有部署，按要求跳过升级或修复命令' ;;
    en-US:operation_skipped) printf 'Existing deployment preserved; upgrade or repair commands were skipped' ;;
    zh-CN:open_panel) printf '打开面板' ;;
    en-US:open_panel) printf 'Open panel' ;;
    zh-CN:admin_key) printf 'CPAMP 管理员密钥' ;;
    en-US:admin_key) printf 'CPAMP Admin Key' ;;
    zh-CN:admin_key_file) printf '管理员密钥文件' ;;
    en-US:admin_key_file) printf 'Admin key file' ;;
    zh-CN:cpa_key_file) printf 'CPA Management Key 文件' ;;
    en-US:cpa_key_file) printf 'CPA Management Key file' ;;
    zh-CN:demo_client_key_file) printf '演示客户端 API Key 文件' ;;
    en-US:demo_client_key_file) printf 'Demo client API key file' ;;
    zh-CN:systemd_file) printf 'systemd service 文件' ;;
    en-US:systemd_file) printf 'systemd service file' ;;
    zh-CN:next_setup) printf '首次打开面板后，在 setup 中填写 CPA 地址和 CPA Management Key。' ;;
    en-US:next_setup) printf 'After opening the panel, enter the CPA URL and CPA Management Key in setup.' ;;
    zh-CN:next_full_stack) printf '完整 Docker 安装已把 CPA URL、CPA Management Key 和演示客户端 API Key 写入配置。打开面板后直接用 CPAMP 管理员密钥登录，不需要首次 setup。' ;;
    en-US:next_full_stack) printf 'The full Docker install configured CPA URL, CPA Management Key, and a demo client API key. Open the panel and log in with the CPAMP Admin Key; first setup is not required.' ;;
    zh-CN:next_env_managed) printf 'CPA 连接已写入本机 secret 并由环境管理。打开面板后直接用 CPAMP 管理员密钥登录；如需修改 CPA 连接，请更新安装目录中的配置和 secret。' ;;
    en-US:next_env_managed) printf 'The CPA connection is stored in local secrets and managed by the environment. Open the panel and log in with the CPAMP Admin Key; update the install directory config and secrets to change the CPA connection.' ;;
    zh-CN:skip_execute) printf '已生成配置，但按要求跳过启动命令。' ;;
    en-US:skip_execute) printf 'Configuration was generated, but start commands were skipped as requested.' ;;
    zh-CN:port_busy) printf '端口可能已被占用' ;;
    en-US:port_busy) printf 'Port may already be in use' ;;
    zh-CN:missing_command) printf '缺少命令' ;;
    en-US:missing_command) printf 'Missing command' ;;
    zh-CN:existing_install) printf '检测到已有 CPA Manager Plus 部署' ;;
    en-US:existing_install) printf 'Existing CPA Manager Plus deployment detected' ;;
    zh-CN:existing_volume) printf '检测到已有 Docker 数据卷' ;;
    en-US:existing_volume) printf 'Existing Docker data volume detected' ;;
    zh-CN:select_existing_action) printf '选择操作: 1) 升级现有部署  2) 修复管理员登录  3) 重新生成配置  4) 退出' ;;
    en-US:select_existing_action) printf 'Select action: 1) upgrade existing deployment  2) repair admin login  3) regenerate config  4) exit' ;;
    zh-CN:select_partial_action) printf '检测到不完整的部署配置。选择操作: 1) 备份并重新生成配置  2) 退出' ;;
    en-US:select_partial_action) printf 'Incomplete deployment config detected. Select action: 1) back up and regenerate config  2) exit' ;;
    zh-CN:select_orphan_action) printf '安装目录缺少配置，但发现旧数据卷。选择操作: 1) 修复并继续使用旧数据  2) 使用新项目名全新安装（旧服务仍运行时请改端口）  3) 退出' ;;
    en-US:select_orphan_action) printf 'The install directory has no config, but an old data volume exists. Select action: 1) repair and keep old data  2) fresh install with a new project name (choose different ports if the old service is still running)  3) exit' ;;
    zh-CN:operation_upgrade) printf '升级现有部署' ;;
    en-US:operation_upgrade) printf 'Upgrade existing deployment' ;;
    zh-CN:operation_repair) printf '修复管理员登录' ;;
    en-US:operation_repair) printf 'Repair admin login' ;;
    zh-CN:operation_regenerate) printf '重新生成部署配置' ;;
    en-US:operation_regenerate) printf 'Regenerate deployment config' ;;
    zh-CN:operation_install) printf '首次安装' ;;
    en-US:operation_install) printf 'Fresh install' ;;
    zh-CN:operation_label) printf '执行操作' ;;
    en-US:operation_label) printf 'Operation' ;;
    zh-CN:noninteractive_existing) printf '检测到已有部署。非交互模式必须设置 CPAMP_OPERATION=upgrade、repair 或 regenerate。' ;;
    en-US:noninteractive_existing) printf 'An existing deployment was detected. Non-interactive mode requires CPAMP_OPERATION=upgrade, repair, or regenerate.' ;;
    zh-CN:orphan_noninteractive) printf '检测到旧 Docker 数据卷但安装目录缺少配置。请设置 CPAMP_OPERATION=repair，或设置新的 CPAMP_PROJECT_NAME 后使用 CPAMP_OPERATION=install。' ;;
    en-US:orphan_noninteractive) printf 'An old Docker data volume exists but the install directory has no config. Set CPAMP_OPERATION=repair, or choose a new CPAMP_PROJECT_NAME with CPAMP_OPERATION=install.' ;;
    zh-CN:orphan_mode_required) printf '非交互修复旧数据卷时必须设置 CPAMP_INSTALL_MODE=stack 或 cpamp，避免创建错误的服务组合。' ;;
    en-US:orphan_mode_required) printf 'Non-interactive orphan-volume repair requires CPAMP_INSTALL_MODE=stack or cpamp to avoid creating the wrong service combination.' ;;
    zh-CN:repair_skip_execute) printf '旧数据卷修复必须执行数据库同步，不能使用 CPAMP_SKIP_EXECUTE=1；如只想预览，请使用 CPAMP_DRY_RUN=1。' ;;
    en-US:repair_skip_execute) printf 'Orphan-volume repair must execute the database sync and cannot use CPAMP_SKIP_EXECUTE=1; use CPAMP_DRY_RUN=1 for a preview.' ;;
    zh-CN:repairing_admin) printf '正在把数据库管理员凭证同步为安装目录中的管理员密钥' ;;
    en-US:repairing_admin) printf 'Synchronizing the database admin credential with the install-directory admin key' ;;
    zh-CN:auth_verified) printf '管理员密钥验证通过' ;;
    en-US:auth_verified) printf 'Admin key verification passed' ;;
    zh-CN:auth_failed) printf 'CPAMP 已启动，但管理员密钥验证失败。数据库凭证可能与安装目录中的密钥不一致。' ;;
    en-US:auth_failed) printf 'CPAMP started, but admin key verification failed. The database credential may not match the install-directory key.' ;;
    zh-CN:auth_repair_prompt) printf '是否停止 CPAMP 并自动修复管理员登录? 输入 yes/no' ;;
    en-US:auth_repair_prompt) printf 'Stop CPAMP and repair the admin login automatically? Enter yes/no' ;;
    zh-CN:health_failed) printf 'CPAMP 容器未能在规定时间内通过健康检查。' ;;
    en-US:health_failed) printf 'The CPAMP container did not become healthy in time.' ;;
    zh-CN:key_saved) printf '管理员密钥已保存' ;;
    en-US:key_saved) printf 'Admin key saved' ;;
    zh-CN:key_view_command) printf '查看管理员密钥' ;;
    en-US:key_view_command) printf 'View admin key' ;;
    zh-CN:key_reveal_prompt) printf '现在在终端显示完整管理员密钥吗? 请勿分享包含密钥的截图。输入 yes/no' ;;
    en-US:key_reveal_prompt) printf 'Show the full admin key in the terminal now? Do not share screenshots containing it. Enter yes/no' ;;
    zh-CN:config_backup) printf '旧配置已备份到' ;;
    en-US:config_backup) printf 'Previous config backed up to' ;;
    zh-CN:project_name) printf 'Docker Compose 项目名' ;;
    en-US:project_name) printf 'Docker Compose project name' ;;
    zh-CN:project_name_empty) printf 'Docker Compose 项目名不能为空。' ;;
    en-US:project_name_empty) printf 'Docker Compose project name must not be empty.' ;;
    zh-CN:docker_unavailable) printf 'Docker daemon 不可用。请启动 Docker 后重新运行安装器。' ;;
    en-US:docker_unavailable) printf 'Docker daemon is not available. Start Docker and run the installer again.' ;;
    zh-CN:missing_config) printf '已有安装配置不完整，请选择重新生成配置。' ;;
    en-US:missing_config) printf 'The existing install directory is incomplete. Choose config regeneration.' ;;
    zh-CN:repair_failed) printf '管理员密钥修复失败，CPAMP 已使用原数据库凭证重新启动。' ;;
    en-US:repair_failed) printf 'Admin key repair failed. CPAMP was restarted with the previous database credential.' ;;
    zh-CN:repair_restart_failed) printf '管理员密钥已重置，但 CPAMP 重启失败。请在安装目录执行 docker compose up -d。' ;;
    en-US:repair_restart_failed) printf 'Admin key reset succeeded, but CPAMP failed to restart. Run docker compose up -d from the install directory.' ;;
    zh-CN:repair_verify_failed) printf '管理员密钥修复后验证仍失败，请确认面板和修复命令使用同一个 Docker 数据卷。' ;;
    en-US:repair_verify_failed) printf 'Admin key repair completed, but verification still failed. Confirm that the panel and repair command use the same Docker volume.' ;;
    *) printf '%s' "$1" ;;
  esac
}

say() {
  printf '%s\n' "$*"
}

require_interactive_tty() {
  if [ "$non_interactive" = "1" ]; then
    return
  fi

  if [ ! -t 0 ]; then
    die "Interactive install requires a terminal on stdin. Download the script and run it with bash, or set CPAMP_NON_INTERACTIVE=1 CPAMP_CONFIRM=1."
  fi

  if [ ! -r /dev/tty ] || [ ! -w /dev/tty ]; then
    die "Interactive install requires access to /dev/tty. Run it from a terminal, or set CPAMP_NON_INTERACTIVE=1 CPAMP_CONFIRM=1."
  fi
}

prompt_line() {
  local prompt="$1"
  local default="$2"
  local answer=""

  if [ "$non_interactive" = "1" ]; then
    printf '%s\n' "$default"
    return
  fi

  if [ -n "$default" ]; then
    printf '%s [%s]: ' "$prompt" "$default" >&2
  else
    printf '%s: ' "$prompt" >&2
  fi
  IFS= read -r answer
  if [ -z "$answer" ]; then
    answer="$default"
  fi
  printf '%s\n' "$answer"
}

prompt_secret() {
  local prompt="$1"
  local env_value="$2"
  local answer=""

  if [ "$non_interactive" = "1" ]; then
    printf '%s\n' "$env_value"
    return
  fi

  printf '%s: ' "$prompt" >&2
  IFS= read -r -s answer
  printf '\n' >&2
  printf '%s\n' "$answer"
}

prompt_choice() {
  local prompt="$1"
  local default="$2"
  local allowed="$3"
  local answer=""

  if [ "$non_interactive" = "1" ]; then
    printf '%s\n' "$default"
    return
  fi

  while true; do
    answer="$(prompt_line "$prompt" "$default")"
    case " $allowed " in
      *" $answer "*) printf '%s\n' "$answer"; return ;;
      *) printf 'Invalid choice: %s\n' "$answer" >&2 ;;
    esac
  done
}

detect_environment() {
  os_name="$(uname -s 2>/dev/null || printf 'unknown')"
  arch_name="$(uname -m 2>/dev/null || printf 'unknown')"

  case "$os_name" in
    Linux) normalized_os="linux" ;;
    Darwin) normalized_os="darwin" ;;
    MINGW*|MSYS*|CYGWIN*) normalized_os="windows" ;;
    *) normalized_os="unknown" ;;
  esac

  case "$arch_name" in
    x86_64|amd64) normalized_arch="amd64" ;;
    arm64|aarch64) normalized_arch="arm64" ;;
    *) normalized_arch="unknown" ;;
  esac

  if [ -r /proc/version ] && grep -qiE 'microsoft|wsl' /proc/version 2>/dev/null; then
    is_wsl="true"
  fi
}

choose_language() {
  local choice=""

  if [ -n "$lang_code" ]; then
    case "$lang_code" in
      zh|zh-CN|cn) lang_code="zh-CN" ;;
      en|en-US) lang_code="en-US" ;;
      *) die "Unsupported CPAMP_LANG: $lang_code" ;;
    esac
    return
  fi

  printf 'Choose language / 选择语言:\n'
  printf '  1) 简体中文\n'
  printf '  2) English\n'
  choice="$(prompt_choice 'Language / 语言' '1' '1 2')"
  case "$choice" in
    2) lang_code="en-US" ;;
    *) lang_code="zh-CN" ;;
  esac
}

show_environment() {
  say "== $(text env_header) =="
  say "$(text os): ${os_name} (${normalized_os})"
  say "$(text arch): ${arch_name} (${normalized_arch})"
  say "$(text wsl): ${is_wsl}"
  if [ "$dry_run" = "1" ]; then
    say "$(text dry_run)"
  fi
}

confirm_environment() {
  local answer=""
  if [ "$non_interactive" = "1" ]; then
    return
  fi
  answer="$(prompt_choice "$(text continue)" "yes" "yes no")"
  [ "$answer" = "yes" ] || exit 0
}

expand_path() {
  case "$1" in
    "~") printf '%s\n' "${HOME:-.}" ;;
    "~/"*) printf '%s/%s\n' "${HOME:-.}" "${1#~/}" ;;
    *) printf '%s\n' "$1" ;;
  esac
}

validate_project_name() {
  local value="$1"
  validate_single_line "$(text project_name)" "$value"
  case "$value" in
    [-_]*|*[!a-z0-9_-]*) die "$(text project_name) contains unsupported characters." ;;
    *) ;;
  esac
}

read_env_value() {
  local file="$1"
  local key="$2"
  local line=""
  local value=""
  local found="0"

  [ -f "$file" ] || return 1
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      "$key="*) value="${line#*=}"; found="1" ;;
    esac
  done < "$file"
  if [ "$found" = "1" ]; then
    printf '%s\n' "$value"
    return 0
  fi
  return 1
}

collect_install_directory() {
  install_dir="$(expand_path "$(prompt_line "$(text install_dir)" "${CPAMP_INSTALL_DIR:-$default_install_dir}")")"
  validate_single_line "$(text install_dir)" "$install_dir"
}

docker_volume_exists() {
  local volume="$1"
  command_exists docker && docker volume inspect "$volume" >/dev/null 2>&1
}

detect_existing_installation() {
  local configured_project=""
  local has_docker_files="0"
  local has_native_files="0"

  if configured_project="$(read_env_value "$install_dir/.env" COMPOSE_PROJECT_NAME 2>/dev/null)"; then
    [ -n "$configured_project" ] || die "$(text project_name_empty)"
    compose_project_name="$configured_project"
  fi
  validate_project_name "$compose_project_name"
  existing_volume_name="${compose_project_name}_cpa-manager-plus-data"

  if [ -e "$install_dir/.env" ] ||
     [ -e "$install_dir/compose.yaml" ] ||
     [ -e "$install_dir/cliproxyapi/config.yaml" ]; then
    has_docker_files="1"
  fi
  if [ -e "$install_dir/run.sh" ] ||
     [ -d "$install_dir/runtime" ] ||
     [ -e "$install_dir/data/usage.sqlite" ]; then
    has_native_files="1"
  fi

  if [ -f "$install_dir/.env" ] && [ -f "$install_dir/compose.yaml" ]; then
    existing_install_state="managed"
  elif [ "$has_docker_files" = "1" ]; then
    existing_install_state="partial"
  elif [ "$has_native_files" = "1" ]; then
    existing_install_state="fresh"
  elif docker_volume_exists "$existing_volume_name"; then
    existing_install_state="orphan-volume"
  elif [ -e "$install_dir/secrets/cpamp-admin-key" ]; then
    existing_install_state="partial"
  else
    existing_install_state="fresh"
  fi

  if [ -e "$install_dir/secrets/cpamp-admin-key" ]; then
    read_existing_secret "$install_dir/secrets/cpamp-admin-key" >/dev/null
  fi
}

normalize_operation() {
  case "$operation" in
    '') ;;
    install|new|fresh) operation="install" ;;
    upgrade|update) operation="upgrade" ;;
    repair|recover|reset-admin-key) operation="repair" ;;
    regenerate|overwrite|reconfigure) operation="regenerate" ;;
    *) die "Unsupported CPAMP_OPERATION: $operation" ;;
  esac
}

choose_new_project_name() {
  compose_project_name="$(prompt_line "$(text project_name)" "${CPAMP_PROJECT_NAME:-cpamp-new}")"
  validate_project_name "$compose_project_name"
  existing_volume_name="${compose_project_name}_cpa-manager-plus-data"
  if docker_volume_exists "$existing_volume_name"; then
    die "Docker volume already exists: $existing_volume_name"
  fi
  operation="install"
  existing_install_state="fresh"
}

resolve_operation() {
  local choice=""

  normalize_operation
  if [ "$existing_install_state" = "fresh" ]; then
    [ -n "$operation" ] || operation="install"
    [ "$operation" = "install" ] || die "CPAMP_OPERATION=$operation requires an existing Docker deployment."
    return
  fi

  if [ -z "$operation" ] && [ "${CPAMP_OVERWRITE:-0}" = "1" ]; then
    operation="regenerate"
  fi

  if [ "$existing_install_state" = "orphan-volume" ]; then
    say ""
    say "== $(text existing_volume) =="
    say "$(text project_name): $compose_project_name"
    say "Docker volume: $existing_volume_name"
    if [ -z "$operation" ]; then
      if [ "$non_interactive" = "1" ]; then
        die "$(text orphan_noninteractive)"
      fi
      choice="$(prompt_choice "$(text select_orphan_action)" "1" "1 2 3")"
      case "$choice" in
        1) operation="repair" ;;
        2) choose_new_project_name; return ;;
        3) exit 0 ;;
      esac
    fi
    case "$operation" in
      repair)
        if [ "$non_interactive" = "1" ] && [ -z "${CPAMP_INSTALL_MODE:-}" ]; then
          die "$(text orphan_mode_required)"
        fi
        if [ "$skip_execute" = "1" ] && [ "$dry_run" != "1" ]; then
          die "$(text repair_skip_execute)"
        fi
        return
        ;;
      install) die "$(text orphan_noninteractive)" ;;
      *) die "CPAMP_OPERATION=$operation is not available without an existing compose.yaml." ;;
    esac
    return
  fi

  if [ "$existing_install_state" = "partial" ]; then
    say ""
    say "== $(text existing_install) =="
    say "$(text directory_label): $install_dir"
    if [ -z "$operation" ]; then
      if [ "$non_interactive" = "1" ]; then
        die "$(text noninteractive_existing)"
      fi
      choice="$(prompt_choice "$(text select_partial_action)" "1" "1 2")"
      case "$choice" in
        1) operation="regenerate" ;;
        2) exit 0 ;;
      esac
    fi
    [ "$operation" = "regenerate" ] || die "$(text missing_config) Set CPAMP_OPERATION=regenerate to rebuild its config."
    return
  fi

  say ""
  say "== $(text existing_install) =="
  say "$(text directory_label): $install_dir"
  say "$(text project_name): $compose_project_name"
  if [ -z "$operation" ]; then
    if [ "$non_interactive" = "1" ]; then
      die "$(text noninteractive_existing)"
    fi
    choice="$(prompt_choice "$(text select_existing_action)" "1" "1 2 3 4")"
    case "$choice" in
      1) operation="upgrade" ;;
      2) operation="repair" ;;
      3) operation="regenerate" ;;
      4) exit 0 ;;
    esac
  fi

  case "$operation" in
    upgrade|repair)
      if [ "$existing_install_state" != "managed" ]; then
        die "$(text missing_config) Set CPAMP_OPERATION=regenerate to rebuild its config."
      fi
      ;;
    regenerate) ;;
    *) die "CPAMP_OPERATION=$operation is not valid for an existing deployment." ;;
  esac
}

read_existing_secret() {
  local file="$1"
  local value=""
  [ -f "$file" ] || return 1
  if [ "$dry_run" != "1" ]; then
    chmod 600 "$file" 2>/dev/null || die "Unable to restrict secret file permissions: $file"
  fi
  value="$(< "$file")"
  value="${value%$'\r'}"
  validate_secret_value "$file" "$value"
  printf '%s\n' "$value"
}

load_existing_docker_config() {
  local value=""

  [ -f "$install_dir/.env" ] || die "Missing existing config: $install_dir/.env"
  [ -f "$install_dir/compose.yaml" ] || die "Missing existing config: $install_dir/compose.yaml"
  deploy_method="docker"
  cpamp_image="$(read_env_value "$install_dir/.env" CPAMP_IMAGE 2>/dev/null || printf '%s' "$default_cpamp_image")"
  validate_image_ref "$(text cpamp_image)" "$cpamp_image"
  cpamp_port="$(read_env_value "$install_dir/.env" CPAMP_PORT 2>/dev/null || printf '18317')"
  normalize_port "$cpamp_port" || die "Invalid CPAMP port in existing .env: $cpamp_port"
  if grep -q '^[[:space:]]*cli-proxy-api:' "$install_dir/compose.yaml"; then
    install_mode="stack"
    cpa_image="$(read_env_value "$install_dir/.env" CPA_IMAGE 2>/dev/null || printf '%s' "$default_cpa_image")"
    validate_image_ref "$(text cpa_image)" "$cpa_image"
    cpa_port="$(read_env_value "$install_dir/.env" CPA_PORT 2>/dev/null || printf '8317')"
    normalize_port "$cpa_port" || die "Invalid CPA port in existing .env: $cpa_port"
    cpa_url="http://cli-proxy-api:8317"
    cpa_connection_mode="env"
  else
    install_mode="cpamp"
    if grep -q 'CPA_MANAGEMENT_KEY_FILE:' "$install_dir/compose.yaml"; then
      cpa_connection_mode="env"
      cpa_url="$(read_env_value "$install_dir/.env" CPA_UPSTREAM_URL 2>/dev/null || true)"
    else
      cpa_connection_mode="setup"
    fi
  fi
  if value="$(read_existing_secret "$install_dir/secrets/cpamp-admin-key")"; then
    admin_key="$value"
  elif [ "$operation" = "repair" ]; then
    admin_secret_missing="1"
  else
    die "Admin key file is missing: $install_dir/secrets/cpamp-admin-key. Run with CPAMP_OPERATION=repair."
  fi
}

ensure_repair_admin_key() {
  if [ "$admin_secret_missing" != "1" ]; then
    return 0
  fi
  generated_admin_key="cpamp_$(random_alnum 32)"
  admin_key="$(ensure_secret_file "$install_dir/secrets/cpamp-admin-key" "$generated_admin_key")"
  admin_secret_missing="0"
}

normalize_port() {
  local value="$1"
  case "$value" in
    ''|*[!0-9]*) return 1 ;;
    *)
      if [ "$value" -lt 1 ] || [ "$value" -gt 65535 ]; then
        return 1
      fi
      ;;
  esac
}

has_line_break() {
  case "$1" in
    *$'\n'*|*$'\r'*) return 0 ;;
    *) return 1 ;;
  esac
}

validate_single_line() {
  local label="$1"
  local value="$2"
  [ -n "$value" ] || die "$label must not be empty."
  if has_line_break "$value"; then
    die "$label must be a single line."
  fi
}

validate_secret_value() {
  validate_single_line "$1" "$2"
}

validate_url_value() {
  local label="$1"
  local value="$2"
  validate_single_line "$label" "$value"
  case "$value" in
    http://*|https://*) ;;
    *) die "$label must start with http:// or https://." ;;
  esac
  if [[ "$value" == *[[:space:]]* ||
        "$value" == *'#'* ||
        "$value" == *'?'* ||
        "$value" == *\"* ||
        "$value" == *"'"* ||
        "$value" == *\\* ||
        "$value" == *'$'* ||
        "$value" == *'`'* ]]; then
    die "$label contains unsupported characters."
  fi
}

validate_image_ref() {
  local label="$1"
  local value="$2"
  validate_single_line "$label" "$value"
  case "$value" in
    *[!A-Za-z0-9._/@:-]*)
      die "$label contains unsupported characters."
      ;;
  esac
}

yaml_double_quote_escape() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  printf '%s\n' "$value"
}

collect_choices() {
  local mode_choice=""
  local method_choice=""
  local conn_choice=""

  mode_choice="${CPAMP_INSTALL_MODE:-${install_mode:-}}"
  if [ -z "$mode_choice" ]; then
    mode_choice="$(prompt_choice "$(text select_mode)" "1" "1 2")"
    case "$mode_choice" in
      1) install_mode="stack" ;;
      2) install_mode="cpamp" ;;
    esac
  else
    install_mode="$mode_choice"
  fi

  method_choice="${CPAMP_DEPLOY_METHOD:-${deploy_method:-}}"
  if [ "$existing_install_state" = "orphan-volume" ] && [ "$operation" = "repair" ]; then
    method_choice="docker"
  fi
  if [ -z "$method_choice" ]; then
    method_choice="$(prompt_choice "$(text select_method)" "1" "1 2")"
    case "$method_choice" in
      1) deploy_method="docker" ;;
      2) deploy_method="native" ;;
    esac
  else
    deploy_method="$method_choice"
  fi

  case "$install_mode" in
    stack|full|all) install_mode="stack" ;;
    cpamp|manager|cpamp-only) install_mode="cpamp" ;;
    *) die "Unsupported CPAMP_INSTALL_MODE: $install_mode" ;;
  esac

  case "$deploy_method" in
    docker|compose) deploy_method="docker" ;;
    native|binary) deploy_method="native" ;;
    *) die "Unsupported CPAMP_DEPLOY_METHOD: $deploy_method" ;;
  esac

  if [ "$install_mode" = "stack" ] && [ "$deploy_method" = "native" ]; then
    say "$(text unsupported_stack_native)"
    if [ "$non_interactive" = "1" ]; then
      exit 1
    fi
    return 1
  fi

  cpamp_port="$(prompt_line "$(text cpamp_port)" "${CPAMP_PORT:-${cpamp_port:-18317}}")"
  normalize_port "$cpamp_port" || die "Invalid CPAMP port: $cpamp_port"

  if [ "$deploy_method" = "docker" ]; then
    cpamp_image="$(prompt_line "$(text cpamp_image)" "${CPAMP_IMAGE:-${cpamp_image:-$default_cpamp_image}}")"
    validate_image_ref "$(text cpamp_image)" "$cpamp_image"
    if [ "$install_mode" = "stack" ]; then
      cpa_port="$(prompt_line "$(text cpa_port)" "${CPAMP_CPA_PORT:-${cpa_port:-8317}}")"
      normalize_port "$cpa_port" || die "Invalid CPA port: $cpa_port"
      cpa_image="$(prompt_line "$(text cpa_image)" "${CPAMP_CPA_IMAGE:-${cpa_image:-$default_cpa_image}}")"
      validate_image_ref "$(text cpa_image)" "$cpa_image"
      cpa_url="http://cli-proxy-api:8317"
      cpa_connection_mode="env"
    fi
  else
    cpamp_version="$(prompt_line "$(text version)" "${CPAMP_VERSION:-latest}")"
    validate_single_line "$(text version)" "$cpamp_version"
  fi

  if [ "$install_mode" = "cpamp" ]; then
    conn_choice="${CPAMP_CPA_CONNECTION_MODE:-${cpa_connection_mode:-}}"
    if [ -z "$conn_choice" ]; then
      if [ "$non_interactive" = "1" ]; then
        cpa_connection_mode="setup"
      else
        conn_choice="$(prompt_choice "$(text cpa_conn_mode)" "1" "1 2")"
        case "$conn_choice" in
          1) cpa_connection_mode="env" ;;
          2) cpa_connection_mode="setup" ;;
        esac
      fi
    else
      cpa_connection_mode="$conn_choice"
    fi

    case "$cpa_connection_mode" in
      setup|panel|later) cpa_connection_mode="setup" ;;
      env|secret|managed) cpa_connection_mode="env" ;;
      *) die "Unsupported CPAMP_CPA_CONNECTION_MODE: $cpa_connection_mode" ;;
    esac

    if [ "$cpa_connection_mode" = "env" ]; then
      local default_cpa_url="http://127.0.0.1:8317"
      if [ "$deploy_method" = "docker" ]; then
        default_cpa_url="http://host.docker.internal:8317"
      fi
      cpa_url="$(prompt_line "$(text cpa_url)" "${CPAMP_CPA_URL:-${cpa_url:-$default_cpa_url}}")"
      validate_url_value "$(text cpa_url)" "$cpa_url"
      cpa_management_key="$(prompt_secret "$(text cpa_key)" "${CPAMP_CPA_MANAGEMENT_KEY:-}")"
      if [ -n "$cpa_management_key" ]; then
        validate_secret_value "$(text cpa_key)" "$cpa_management_key"
      fi
    fi
  fi
}

print_summary() {
  say ""
  say "== $(text summary) =="
  say "$(text operation_label): $(text "operation_${operation}")"
  if [ "$install_mode" = "stack" ]; then
    say "$(text install_mode_label): $(text stack_mode)"
  else
    say "$(text install_mode_label): $(text cpamp_mode)"
  fi
  if [ "$deploy_method" = "docker" ]; then
    say "$(text deploy_method_label): $(text docker_method)"
  else
    say "$(text deploy_method_label): $(text native_method)"
  fi
  say "$(text directory_label): $install_dir"
  if [ "$deploy_method" = "docker" ]; then
    say "$(text project_name): $compose_project_name"
  fi
  say "$(text cpamp_port): $cpamp_port"
  if [ "$deploy_method" = "docker" ]; then
    say "$(text cpamp_image): $cpamp_image"
    if [ "$install_mode" = "stack" ]; then
      say "$(text cpa_image): $cpa_image"
      say "$(text cpa_port): $cpa_port"
      say "$(text cpa_url_for_cpamp): $cpa_url"
      say "$(text cpa_connection_label): $(text cpa_connection_env)"
    else
      if [ "$cpa_connection_mode" = "env" ]; then
        say "$(text cpa_connection_label): $(text cpa_connection_env)"
      else
        say "$(text cpa_connection_label): $(text cpa_connection_setup)"
      fi
      if [ "$cpa_connection_mode" = "env" ]; then
        say "$(text cpa_url): $cpa_url"
      fi
    fi
  else
    say "$(text version): $cpamp_version"
    if [ "$cpa_connection_mode" = "env" ]; then
      say "$(text cpa_connection_label): $(text cpa_connection_env)"
    else
      say "$(text cpa_connection_label): $(text cpa_connection_setup)"
    fi
    if [ "$cpa_connection_mode" = "env" ]; then
      say "$(text cpa_url): $cpa_url"
    fi
  fi
}

confirm_choices() {
  local answer=""

  if [ "$non_interactive" = "1" ]; then
    if [ "$dry_run" = "1" ] || [ "${CPAMP_CONFIRM:-0}" = "1" ]; then
      return
    fi
    die "Set CPAMP_CONFIRM=1 to execute non-interactively."
  fi

  answer="$(prompt_choice "$(text confirm)" "confirm" "confirm modify abort")"
  case "$answer" in
    confirm) return 0 ;;
    modify) return 1 ;;
    abort) exit 0 ;;
  esac
}

command_exists() {
  command -v "$1" >/dev/null 2>&1
}

check_port() {
  local port="$1"
  if command_exists lsof && lsof -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
    say "$(text port_busy): $port"
  fi
}

require_command() {
  local name="$1"
  if ! command_exists "$name"; then
    if [ "$dry_run" = "1" ] || [ "$skip_execute" = "1" ]; then
      say "$(text missing_command): $name"
    else
      die "$(text missing_command): $name"
    fi
  fi
}

check_requirements() {
  check_port "$cpamp_port"
  if [ "$install_mode" = "stack" ]; then
    check_port "$cpa_port"
  fi

  if [ "$deploy_method" = "docker" ]; then
    require_command docker
    if [ "$dry_run" != "1" ] && [ "$skip_execute" != "1" ]; then
      if ! docker compose version >/dev/null 2>&1; then
        die "docker compose is required."
      fi
      if ! docker info >/dev/null 2>&1; then
        die "$(text docker_unavailable)"
      fi
    fi
  else
    case "$normalized_os" in
      linux|darwin) ;;
      *) die "Native install supports Linux and macOS in this script." ;;
    esac
    case "$normalized_arch" in
      amd64|arm64) ;;
      *) die "Unsupported architecture for native package: $arch_name" ;;
    esac
    require_command curl
    if [ "$normalized_os" = "darwin" ] || [ "$normalized_os" = "linux" ]; then
      require_command tar
    fi
  fi
}

random_alnum() {
  local length="${1:-32}"
  local value=""
  local candidate=""
  local empty_attempts=0
  local max_empty_attempts=32

  while [ "${#value}" -lt "$length" ]; do
    if command_exists openssl; then
      candidate="$(openssl rand -base64 96 | LC_ALL=C tr -dc 'A-Za-z0-9')"
    elif [ -r /dev/urandom ] && command_exists dd; then
      candidate="$(dd if=/dev/urandom bs=256 count=1 2>/dev/null | LC_ALL=C tr -dc 'A-Za-z0-9')"
    else
      die "openssl or /dev/urandom is required to generate secure keys."
    fi
    if [ -z "$candidate" ]; then
      empty_attempts=$((empty_attempts + 1))
      if [ "$empty_attempts" -ge "$max_empty_attempts" ]; then
        die "Random source produced no usable alphanumeric characters."
      fi
      continue
    fi
    value="${value}${candidate}"
  done

  printf '%s\n' "${value:0:$length}"
}

ensure_dir() {
  local dir="$1"
  if [ "$dry_run" = "1" ]; then
    return
  fi
  mkdir -p "$dir"
}

overwrite_enabled() {
  [ "${CPAMP_OVERWRITE:-0}" = "1" ] || [ "$operation" = "regenerate" ]
}

backup_generated_config() {
  local backup_dir=""
  local source=""
  local relative=""

  if [ "$operation" != "regenerate" ] || [ "$dry_run" = "1" ]; then
    return 0
  fi
  backup_dir="$install_dir/backups/installer-$(date '+%Y%m%d-%H%M%S')"
  for relative in .env compose.yaml cliproxyapi/config.yaml run.sh cpa-manager-plus.service; do
    source="$install_dir/$relative"
    [ -e "$source" ] || continue
    mkdir -p "$backup_dir/$(dirname "$relative")"
    cp -p "$source" "$backup_dir/$relative"
  done
  if [ -d "$backup_dir" ]; then
    say "$(text config_backup): $backup_dir"
  fi
}

prepare_file() {
  local file="$1"
  if [ -e "$file" ] && ! overwrite_enabled; then
    die "File already exists: $file. Set CPAMP_OVERWRITE=1 if you want to overwrite generated config files."
  fi
  mkdir -p "$(dirname "$file")"
}

preflight_file_write() {
  local file="$1"
  if [ "$dry_run" = "1" ]; then
    return
  fi
  if [ -e "$file" ] && ! overwrite_enabled; then
    die "File already exists: $file. Set CPAMP_OVERWRITE=1 if you want to overwrite generated config files."
  fi
}

preflight_native_binary_dir() {
  local dir="$1"
  if [ "$dry_run" = "1" ]; then
    return
  fi
  if [ -d "$dir" ] && ! overwrite_enabled; then
    die "Directory already exists: $dir. Set CPAMP_OVERWRITE=1 if you want to reuse it."
  fi
}

ensure_secret_file() {
  local file="$1"
  local value="$2"
  local existing=""

  if [ "$dry_run" = "1" ]; then
    if [ -f "$file" ]; then
      existing="$(< "$file")"
      existing="${existing%$'\r'}"
      validate_secret_value "$file" "$existing"
      printf '%s\n' "$existing"
      return
    fi
    validate_secret_value "$file" "$value"
    printf '%s: %s\n' "$(text write_file)" "$file" >&2
    printf '%s\n' "$value"
    return
  fi

  mkdir -p "$(dirname "$file")"
  if [ -f "$file" ]; then
    chmod 600 "$file" 2>/dev/null || die "Unable to restrict secret file permissions: $file"
    existing="$(< "$file")"
    existing="${existing%$'\r'}"
    validate_secret_value "$file" "$existing"
    printf '%s\n' "$existing"
    return
  fi

  validate_secret_value "$file" "$value"
  printf '%s\n' "$value" > "$file"
  chmod 600 "$file"
  printf '%s\n' "$value"
}

write_env_file() {
  local file="$install_dir/.env"
  local tmp="${file}.tmp.$$"
  if [ "$dry_run" = "1" ]; then
    say "$(text write_file): $file"
    return
  fi
  prepare_file "$file"
  {
    printf 'COMPOSE_PROJECT_NAME=%s\n' "$compose_project_name"
    printf 'CPAMP_IMAGE=%s\n' "$cpamp_image"
    printf 'CPAMP_PORT=%s\n' "$cpamp_port"
    if [ "$install_mode" = "stack" ]; then
      printf 'CPA_IMAGE=%s\n' "$cpa_image"
      printf 'CPA_PORT=%s\n' "$cpa_port"
    elif [ "$cpa_connection_mode" = "env" ]; then
      printf 'CPA_UPSTREAM_URL=%s\n' "$cpa_url"
    fi
  } > "$tmp"
  mv -f "$tmp" "$file"
}

write_cpa_config() {
  local file="$install_dir/cliproxyapi/config.yaml"
  local tmp="${file}.tmp.$$"
  local escaped_cpa_management_key=""
  local escaped_demo_client_key=""
  if [ "$dry_run" = "1" ]; then
    say "$(text write_file): $file"
    return
  fi
  prepare_file "$file"
  escaped_cpa_management_key="$(yaml_double_quote_escape "$cpa_management_key")"
  escaped_demo_client_key="$(yaml_double_quote_escape "$demo_client_key")"
  cat > "$tmp" <<EOF
host: "0.0.0.0"
port: 8317

remote-management:
  secret-key: "$escaped_cpa_management_key"
  allow-remote: true
  disable-control-panel: false
  disable-auto-update-panel: true
  panel-github-repository: "https://github.com/seakee/CPA-Manager-Plus"

usage-statistics-enabled: true
redis-usage-queue-retention-seconds: 60

auth-dir: "/root/.cli-proxy-api"

api-keys:
  - "$escaped_demo_client_key"
EOF
  mv -f "$tmp" "$file"
}

docker_needs_host_gateway() {
  [ "$deploy_method" = "docker" ] &&
    [ "$install_mode" = "cpamp" ] &&
    [ "$cpa_connection_mode" = "env" ] &&
    [ "$normalized_os" = "linux" ] &&
    case "$cpa_url" in
      *host.docker.internal*) true ;;
      *) false ;;
    esac
}

write_docker_compose() {
  local file="$install_dir/compose.yaml"
  local tmp="${file}.tmp.$$"
  if [ "$dry_run" = "1" ]; then
    say "$(text write_file): $file"
    return
  fi
  prepare_file "$file"

  if [ "$install_mode" = "stack" ]; then
    cat > "$tmp" <<'EOF'
services:
  cli-proxy-api:
    image: ${CPA_IMAGE}
    restart: unless-stopped
    ports:
      - "${CPA_PORT}:8317"
    volumes:
      - ./cliproxyapi/config.yaml:/CLIProxyAPI/config.yaml
      - ./cliproxyapi/auths:/root/.cli-proxy-api
      - ./cliproxyapi/logs:/CLIProxyAPI/logs

  cpa-manager-plus:
    image: ${CPAMP_IMAGE}
    restart: unless-stopped
    ports:
      - "${CPAMP_PORT}:18317"
    environment:
      HTTP_ADDR: "0.0.0.0:18317"
      USAGE_DB_PATH: "/data/usage.sqlite"
      CPA_MANAGER_DATA_KEY_PATH: "/data/data.key"
      CPA_MANAGER_ADMIN_KEY_FILE: "/run/secrets/cpamp_admin_key"
      CPA_UPSTREAM_URL: "http://cli-proxy-api:8317"
      CPA_MANAGEMENT_KEY_FILE: "/run/secrets/cpa_management_key"
      USAGE_COLLECTOR_MODE: "auto"
      USAGE_RESP_QUEUE: "usage"
      USAGE_RESP_POP_SIDE: "right"
      USAGE_BATCH_SIZE: "100"
      USAGE_POLL_INTERVAL_MS: "500"
      USAGE_QUERY_LIMIT: "50000"
      USAGE_CORS_ORIGINS: "*"
    volumes:
      - cpa-manager-plus-data:/data
    secrets:
      - cpamp_admin_key
      - cpa_management_key
    depends_on:
      - cli-proxy-api
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:18317/health"]
      interval: 10s
      timeout: 3s
      retries: 3

volumes:
  cpa-manager-plus-data:

secrets:
  cpamp_admin_key:
    file: ./secrets/cpamp-admin-key
  cpa_management_key:
    file: ./secrets/cpa-management-key
EOF
  elif [ "$cpa_connection_mode" = "env" ]; then
    cat > "$tmp" <<'EOF'
services:
  cpa-manager-plus:
    image: ${CPAMP_IMAGE}
    restart: unless-stopped
    ports:
      - "${CPAMP_PORT}:18317"
EOF
    if docker_needs_host_gateway; then
      cat >> "$tmp" <<'EOF'
    extra_hosts:
      - "host.docker.internal:host-gateway"
EOF
    fi
    cat >> "$tmp" <<'EOF'
    environment:
      HTTP_ADDR: "0.0.0.0:18317"
      USAGE_DB_PATH: "/data/usage.sqlite"
      CPA_MANAGER_DATA_KEY_PATH: "/data/data.key"
      CPA_MANAGER_ADMIN_KEY_FILE: "/run/secrets/cpamp_admin_key"
      CPA_UPSTREAM_URL: "${CPA_UPSTREAM_URL}"
      CPA_MANAGEMENT_KEY_FILE: "/run/secrets/cpa_management_key"
      USAGE_COLLECTOR_MODE: "auto"
      USAGE_RESP_QUEUE: "usage"
      USAGE_RESP_POP_SIDE: "right"
      USAGE_BATCH_SIZE: "100"
      USAGE_POLL_INTERVAL_MS: "500"
      USAGE_QUERY_LIMIT: "50000"
      USAGE_CORS_ORIGINS: "*"
    volumes:
      - cpa-manager-plus-data:/data
    secrets:
      - cpamp_admin_key
      - cpa_management_key
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:18317/health"]
      interval: 10s
      timeout: 3s
      retries: 3

volumes:
  cpa-manager-plus-data:

secrets:
  cpamp_admin_key:
    file: ./secrets/cpamp-admin-key
  cpa_management_key:
    file: ./secrets/cpa-management-key
EOF
  else
    cat > "$tmp" <<'EOF'
services:
  cpa-manager-plus:
    image: ${CPAMP_IMAGE}
    restart: unless-stopped
    ports:
      - "${CPAMP_PORT}:18317"
    environment:
      HTTP_ADDR: "0.0.0.0:18317"
      USAGE_DB_PATH: "/data/usage.sqlite"
      CPA_MANAGER_DATA_KEY_PATH: "/data/data.key"
      CPA_MANAGER_ADMIN_KEY_FILE: "/run/secrets/cpamp_admin_key"
      USAGE_COLLECTOR_MODE: "auto"
      USAGE_RESP_QUEUE: "usage"
      USAGE_RESP_POP_SIDE: "right"
      USAGE_BATCH_SIZE: "100"
      USAGE_POLL_INTERVAL_MS: "500"
      USAGE_QUERY_LIMIT: "50000"
      USAGE_CORS_ORIGINS: "*"
    volumes:
      - cpa-manager-plus-data:/data
    secrets:
      - cpamp_admin_key
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:18317/health"]
      interval: 10s
      timeout: 3s
      retries: 3

volumes:
  cpa-manager-plus-data:

secrets:
  cpamp_admin_key:
    file: ./secrets/cpamp-admin-key
EOF
  fi
  mv -f "$tmp" "$file"
}

preflight_docker_files() {
  preflight_file_write "$install_dir/.env"
  preflight_file_write "$install_dir/compose.yaml"
  if [ "$install_mode" = "stack" ]; then
    preflight_file_write "$install_dir/cliproxyapi/config.yaml"
  fi
}

generate_docker_files() {
  preflight_docker_files

  ensure_dir "$install_dir"
  ensure_dir "$install_dir/secrets"
  ensure_dir "$install_dir/cliproxyapi/auths"
  ensure_dir "$install_dir/cliproxyapi/logs"

  generated_admin_key="cpamp_$(random_alnum 32)"
  admin_key="$(ensure_secret_file "$install_dir/secrets/cpamp-admin-key" "$generated_admin_key")"

  write_env_file

  if [ "$install_mode" = "stack" ]; then
    generated_cpa_management_key="cpa_$(random_alnum 32)"
    cpa_management_key="$(ensure_secret_file "$install_dir/secrets/cpa-management-key" "$generated_cpa_management_key")"
    generated_demo_client_key="sk-$(random_alnum 64)"
    demo_client_key="$(ensure_secret_file "$install_dir/secrets/cpa-demo-client-key" "$generated_demo_client_key")"
    write_cpa_config
  elif [ "$cpa_connection_mode" = "env" ]; then
    ensure_secret_file "$install_dir/secrets/cpa-management-key" "$cpa_management_key" >/dev/null
  fi

  write_docker_compose
}

run_docker_install() {
  if [ "$dry_run" = "1" ]; then
    say "$(text run_command): cd \"$install_dir\" && docker compose pull && docker compose up -d"
    return
  fi
  if [ "$skip_execute" = "1" ]; then
    say "$(text skip_execute)"
    say "cd \"$install_dir\" && docker compose pull && docker compose up -d"
    return
  fi
  (
    cd "$install_dir"
    docker compose pull
    docker compose up -d
  )
}

run_docker_repair() {
  if [ "$dry_run" = "1" ]; then
    say "$(text run_command): cd \"$install_dir\" && docker compose stop cpa-manager-plus"
    say "$(text run_command): docker compose run --rm cpa-manager-plus reset-admin-key --admin-key-file /run/secrets/cpamp_admin_key"
    say "$(text run_command): docker compose up -d"
    return
  fi
  if [ "$skip_execute" = "1" ]; then
    say "$(text skip_execute)"
    return
  fi
  say "$(text repairing_admin)"
  (
    cd "$install_dir"
    if [ "$existing_install_state" = "orphan-volume" ]; then
      docker compose pull
    fi
    docker compose stop cpa-manager-plus
    if ! docker compose run --rm cpa-manager-plus reset-admin-key --admin-key-file /run/secrets/cpamp_admin_key; then
      docker compose up -d || true
      die "$(text repair_failed)"
    fi
    if ! docker compose up -d; then
      die "$(text repair_restart_failed)"
    fi
  )
}

wait_docker_health() {
  local attempts=30
  local i=1

  while [ "$i" -le "$attempts" ]; do
    if (
      cd "$install_dir"
      docker compose exec -T cpa-manager-plus wget -qO- http://127.0.0.1:18317/health >/dev/null 2>&1
    ); then
      return
    fi
    sleep 2
    i=$((i + 1))
  done
  return 1
}

verify_docker_admin_key() {
  [ -n "$admin_key" ] || return 1
  (
    cd "$install_dir"
    docker compose exec -T cpa-manager-plus wget -qO- \
      --header="Authorization: Bearer $admin_key" \
      http://127.0.0.1:18317/status >/dev/null 2>&1
  )
}

validate_docker_install() {
  local answer=""

  if [ "$dry_run" = "1" ] || [ "$skip_execute" = "1" ]; then
    auth_validation_status="skipped"
    return
  fi
  if ! wait_docker_health; then
    die "$(text health_failed) Run 'cd \"$install_dir\" && docker compose logs cpa-manager-plus' for details."
  fi
  if verify_docker_admin_key; then
    auth_validation_status="verified"
    return
  fi

  say "$(text auth_failed)" >&2
  if [ "$operation" = "repair" ]; then
    die "$(text repair_verify_failed)"
  fi
  if [ "$non_interactive" = "1" ]; then
    die "Run again with CPAMP_OPERATION=repair to synchronize the admin credential without deleting data."
  fi
  answer="$(prompt_choice "$(text auth_repair_prompt)" "yes" "yes no")"
  if [ "$answer" != "yes" ]; then
    die "Run the installer again and choose repair admin login."
  fi
  operation="repair"
  run_docker_repair
  if ! wait_docker_health || ! verify_docker_admin_key; then
    die "$(text repair_verify_failed)"
  fi
  auth_validation_status="verified"
}

resolve_latest_version() {
  local version="$cpamp_version"
  local effective_url=""
  if [ "$version" != "latest" ]; then
    printf '%s\n' "$version"
    return
  fi
  if [ "$dry_run" = "1" ]; then
    printf '%s\n' "${CPAMP_VERSION_RESOLVED:-vX.Y.Z}"
    return
  fi
  effective_url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/${repo}/releases/latest")"
  printf '%s\n' "${effective_url##*/}"
}

write_native_config() {
  local binary_dir="$1"
  local file="$binary_dir/config.json"
  local tmp="${file}.tmp.$$"
  if [ "$dry_run" = "1" ]; then
    say "$(text write_file): $file"
    return
  fi
  prepare_file "$file"
  if [ "$cpa_connection_mode" = "env" ]; then
    cat > "$tmp" <<EOF
{
  "httpAddr": "0.0.0.0:$cpamp_port",
  "dataDir": "../../data",
  "adminKeyFile": "../../secrets/cpamp-admin-key",
  "dataKeyPath": "../../data/data.key",
  "cpaUpstreamUrl": "$cpa_url",
  "managementKeyFile": "../../secrets/cpa-management-key",
  "collectorMode": "auto",
  "queue": "usage",
  "popSide": "right",
  "batchSize": 100,
  "pollIntervalMs": 500,
  "queryLimit": 50000
}
EOF
  else
    cat > "$tmp" <<EOF
{
  "httpAddr": "0.0.0.0:$cpamp_port",
  "dataDir": "../../data",
  "adminKeyFile": "../../secrets/cpamp-admin-key",
  "dataKeyPath": "../../data/data.key",
  "collectorMode": "auto",
  "queue": "usage",
  "popSide": "right",
  "batchSize": 100,
  "pollIntervalMs": 500,
  "queryLimit": 50000
}
EOF
  fi
  mv -f "$tmp" "$file"
}

write_native_run_script() {
  local binary_dir="$1"
  local file="$install_dir/run.sh"
  local tmp="${file}.tmp.$$"
  if [ "$dry_run" = "1" ]; then
    say "$(text write_file): $file"
    return
  fi
  prepare_file "$file"
  cat > "$tmp" <<EOF
#!/usr/bin/env bash
set -euo pipefail
cd "$binary_dir"
exec ./cpa-manager-plus
EOF
  mv -f "$tmp" "$file"
  chmod 755 "$file"
}

preflight_native_files() {
  local binary_dir="$1"
  preflight_native_binary_dir "$binary_dir"
  preflight_file_write "$binary_dir/config.json"
  preflight_file_write "$install_dir/run.sh"
  if [ "$normalized_os" = "linux" ]; then
    preflight_file_write "$install_dir/cpa-manager-plus.service"
  fi
}

write_native_systemd_service() {
  local binary_dir="$1"
  local file="$install_dir/cpa-manager-plus.service"
  local tmp="${file}.tmp.$$"
  if [ "$normalized_os" != "linux" ]; then
    return
  fi
  if [ "$dry_run" = "1" ]; then
    say "$(text write_file): $file"
    return
  fi
  prepare_file "$file"
  cat > "$tmp" <<EOF
[Unit]
Description=CPA Manager Plus
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
WorkingDirectory=$binary_dir
ExecStart=$binary_dir/cpa-manager-plus
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
EOF
  mv -f "$tmp" "$file"
}

generate_native_files() {
  local version=""
  local package=""
  local ext="tar.gz"
  local archive=""
  local asset_url=""
  local runtime_dir="$install_dir/runtime"
  local binary_dir=""

  version="$(resolve_latest_version)"
  package="cpa-manager-plus_${version}_${normalized_os}_${normalized_arch}"
  archive="$install_dir/downloads/${package}.${ext}"
  asset_url="https://github.com/${repo}/releases/download/${version}/${package}.${ext}"
  binary_dir="$runtime_dir/$package"

  preflight_native_files "$binary_dir"

  ensure_dir "$install_dir"
  ensure_dir "$install_dir/secrets"
  ensure_dir "$install_dir/data"
  ensure_dir "$install_dir/downloads"
  ensure_dir "$runtime_dir"

  generated_admin_key="cpamp_$(random_alnum 32)"
  admin_key="$(ensure_secret_file "$install_dir/secrets/cpamp-admin-key" "$generated_admin_key")"
  if [ "$cpa_connection_mode" = "env" ]; then
    ensure_secret_file "$install_dir/secrets/cpa-management-key" "$cpa_management_key" >/dev/null
  fi

  if [ "$dry_run" = "1" ]; then
    say "$(text run_command): curl -fL \"$asset_url\" -o \"$archive\""
    say "$(text run_command): tar -xzf \"$archive\" -C \"$runtime_dir\""
    write_native_config "$binary_dir"
    write_native_run_script "$binary_dir"
    write_native_systemd_service "$binary_dir"
    return
  fi

  if [ "$skip_execute" != "1" ]; then
    curl -fL "$asset_url" -o "$archive"
    tar -xzf "$archive" -C "$runtime_dir"
  else
    say "$(text skip_execute)"
    say "curl -fL \"$asset_url\" -o \"$archive\""
    say "tar -xzf \"$archive\" -C \"$runtime_dir\""
  fi

  write_native_config "$binary_dir"
  write_native_run_script "$binary_dir"
  write_native_systemd_service "$binary_dir"
}

print_log_tail() {
  local log_file="$1"
  if [ -s "$log_file" ] && command_exists tail; then
    printf 'Native CPAMP log tail (%s):\n' "$log_file" >&2
    tail -n 80 "$log_file" >&2 || true
  else
    printf 'Native CPAMP log file: %s\n' "$log_file" >&2
  fi
}

wait_native_health() {
  local pid="$1"
  local log_file="$2"
  local health_url="http://127.0.0.1:${cpamp_port}/health"
  local attempts=20
  local i=1

  while [ "$i" -le "$attempts" ]; do
    if ! kill -0 "$pid" >/dev/null 2>&1; then
      print_log_tail "$log_file"
      die "Native CPAMP process exited before becoming healthy. Check the log file: $log_file"
    fi
    if command_exists curl && curl -fsS "$health_url" >/dev/null 2>&1; then
      return
    fi
    sleep 0.5
    i=$((i + 1))
  done

  if ! command_exists curl; then
    printf 'curl is not available; native health endpoint was not checked.\n' >&2
    return
  fi

  print_log_tail "$log_file"
  die "Native CPAMP did not become healthy at $health_url. Check the log file: $log_file"
}

run_native_install() {
  local pid_file="$install_dir/cpa-manager-plus.pid"
  local log_file="$install_dir/cpa-manager-plus.log"
  local pid=""

  if [ "$dry_run" = "1" ]; then
    say "$(text run_command): nohup \"$install_dir/run.sh\" >> \"$log_file\" 2>&1 &"
    return
  fi
  if [ "$skip_execute" = "1" ]; then
    say "$(text skip_execute)"
    return
  fi
  if [ -f "$pid_file" ] && kill -0 "$(cat "$pid_file")" >/dev/null 2>&1; then
    say "CPAMP is already running with PID $(cat "$pid_file")."
    return
  fi
  nohup "$install_dir/run.sh" >> "$log_file" 2>&1 &
  pid="$!"
  printf '%s\n' "$pid" > "$pid_file"
  wait_native_health "$pid" "$log_file"
}

post_install_message() {
  local reveal=""
  say ""
  if [ "$dry_run" = "1" ]; then
    say "== $(text dry_run_done) =="
  elif [ "$skip_execute" = "1" ] && { [ "$operation" = "install" ] || [ "$operation" = "regenerate" ]; }; then
    say "== $(text config_done) =="
  elif [ "$skip_execute" = "1" ]; then
    say "== $(text operation_skipped) =="
  else
    say "== $(text done) =="
  fi
  say "$(text operation_label): $(text "operation_${operation}")"
  if [ "$dry_run" = "1" ] || [ "$admin_secret_missing" = "1" ]; then
    say "$(text admin_key_file): $install_dir/secrets/cpamp-admin-key"
  else
    say "$(text key_saved): $install_dir/secrets/cpamp-admin-key"
    say "$(text key_view_command): cat \"$install_dir/secrets/cpamp-admin-key\""
  fi
  if [ "$dry_run" != "1" ] && [ "$skip_execute" != "1" ]; then
    say "$(text open_panel): http://127.0.0.1:${cpamp_port}/management.html"
  fi
  if [ "$auth_validation_status" = "verified" ]; then
    say "$(text auth_verified)"
  fi
  if [ -n "$admin_key" ] && [ "$non_interactive" != "1" ] && [ "$dry_run" != "1" ] && [ "$skip_execute" != "1" ]; then
    reveal="$(prompt_choice "$(text key_reveal_prompt)" "no" "yes no")"
    if [ "$reveal" = "yes" ]; then
      say "$(text admin_key): $admin_key"
    fi
  fi
  if [ "$install_mode" = "stack" ]; then
    say "$(text cpa_key_file): $install_dir/secrets/cpa-management-key"
    say "$(text demo_client_key_file): $install_dir/secrets/cpa-demo-client-key"
    if [ "$dry_run" != "1" ] && [ "$skip_execute" != "1" ]; then
      say "$(text next_full_stack)"
    fi
  elif [ "$cpa_connection_mode" = "setup" ]; then
    if [ "$dry_run" != "1" ] && [ "$skip_execute" != "1" ]; then
      say "$(text next_setup)"
    fi
  else
    say "$(text cpa_key_file): $install_dir/secrets/cpa-management-key"
    if [ "$dry_run" != "1" ] && [ "$skip_execute" != "1" ]; then
      say "$(text next_env_managed)"
    fi
  fi
  if [ "$deploy_method" = "native" ] && [ "$normalized_os" = "linux" ]; then
    say "$(text systemd_file): $install_dir/cpa-manager-plus.service"
  fi
}

main() {
  detect_environment
  require_interactive_tty
  if [ -z "$lang_code" ] && [ "$non_interactive" != "1" ]; then
    show_environment
  fi
  choose_language
  show_environment
  confirm_environment

  collect_install_directory
  detect_existing_installation
  resolve_operation
  export COMPOSE_PROJECT_NAME="$compose_project_name"

  if [ "$operation" = "upgrade" ] || { [ "$operation" = "repair" ] && [ "$existing_install_state" = "managed" ]; }; then
    load_existing_docker_config
    print_summary
    check_requirements
    if [ "$operation" = "upgrade" ]; then
      run_docker_install
    else
      if [ "$dry_run" != "1" ] && [ "$skip_execute" != "1" ]; then
        ensure_repair_admin_key
      fi
      run_docker_repair
    fi
    validate_docker_install
    post_install_message
    return
  fi

  if [ "$operation" = "regenerate" ] && [ "$existing_install_state" = "managed" ]; then
    load_existing_docker_config
  fi

  while true; do
    collect_choices || continue
    print_summary
    if confirm_choices; then
      break
    fi
  done

  check_requirements

  if [ "$deploy_method" = "docker" ]; then
    backup_generated_config
    generate_docker_files
    if [ "$operation" = "repair" ]; then
      run_docker_repair
    else
      run_docker_install
    fi
    validate_docker_install
  else
    backup_generated_config
    generate_native_files
    run_native_install
  fi

  post_install_message
}

main "$@"
