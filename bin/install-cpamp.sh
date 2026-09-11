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
cpa_management_key_file=""
cpa_management_key_cleanup_allowed="0"
cpa_management_key_inline_input="0"
cpa_management_key_import_copy="0"
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
cpa_connection_imported="0"
cpa_import_retry_state_required="0"
cpa_import_retry_state_loaded="0"
cpa_import_retry_state_file=""
# Pending finalization from an earlier installer run: the installer-managed
# plaintext CPA Management Key still exists while the runtime config no longer
# references it. It is removed only after the stored SQLite connection is
# re-verified through the CPA proxy during this run.
installer_managed_cpa_key_pending_cleanup="0"
installer_managed_cpa_key_pending_file=""
installer_managed_cpa_key_pending_import_copies=""
cpa_proxy_validation_passed="0"
cpa_connection_rollback_pending="0"
legacy_compose_backup=""
legacy_env_backup=""
legacy_cpa_runtime_config_modified="0"
docker_data_snapshot_dir=""
docker_data_snapshot_created="0"
docker_db_path="/data/usage.sqlite"
docker_data_key_path="/data/data.key"
docker_data_volume_root="/data"
docker_compose_config_loaded="0"
docker_compose_config_source=""
native_binary_dir=""
native_existing_binary_dir=""
native_existing_config_file=""
native_existing_config_dir=""
native_data_dir=""
native_db_path=""
native_data_key_path=""
native_admin_key_file=""
native_config_data_dir=""
native_config_db_path=""
native_config_data_key_path=""
native_config_db_declared="0"
native_config_data_key_declared="0"
native_run_data_dir=""
native_run_db_path=""
native_run_data_key_path=""
native_run_data_dir_declared="0"
native_run_db_path_declared="0"
native_run_data_key_path_declared="0"
native_service_data_dir=""
native_service_db_path=""
native_service_data_key_path=""
native_service_data_dir_declared="0"
native_service_db_path_declared="0"
native_service_data_key_path_declared="0"
native_effective_data_dir=""
native_effective_db_path=""
native_effective_data_key_path=""
native_upgrade_backup_dir=""
native_upgrade_snapshot_dir=""
native_upgrade_snapshot_created="0"
native_upgrade_service_existed="0"
native_upgrade_run_script_existed="0"
native_upgrade_data_mutated="0"
native_previous_process_was_running="0"
native_upgrade_switch_applied="0"
native_upgrade_rollback_pending="0"
native_spawned_pid=""
native_spawned_pid_start=""
native_upgrade_log_file=""
installer_exit_rollback_in_progress="0"
installer_script_path="${BASH_SOURCE[0]:-$0}"
case "$installer_script_path" in
  /*) ;;
  *) installer_script_path="$PWD/$installer_script_path" ;;
esac

die() {
  local message="$*"
  if [ "${native_upgrade_rollback_pending:-0}" = "1" ]; then
    rollback_native_upgrade || true
  elif [ "${cpa_connection_rollback_pending:-0}" = "1" ]; then
    rollback_legacy_cpa_runtime_config || true
  fi
  printf '%s\n' "$message" >&2
  exit 1
}

handle_installer_exit() {
  local status="$?"
  trap - EXIT
  if [ "$status" -eq 0 ] || [ "${installer_exit_rollback_in_progress:-0}" = "1" ]; then
    exit "$status"
  fi
  installer_exit_rollback_in_progress="1"
  set +e
  if [ "${native_upgrade_rollback_pending:-0}" = "1" ]; then
    rollback_native_upgrade || true
  elif [ "${cpa_connection_rollback_pending:-0}" = "1" ]; then
    rollback_legacy_cpa_runtime_config || true
  fi
  exit "$status"
}

trap handle_installer_exit EXIT

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
    zh-CN:cpa_connection_env) printf '现在一次性导入 SQLite 加密存储，跳过首次 setup' ;;
    en-US:cpa_connection_env) printf 'import once into encrypted SQLite storage and skip first setup' ;;
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
    zh-CN:next_full_stack) printf '完整 Docker 安装已将 CPA 连接导入 SQLite 加密存储，并配置演示客户端 API Key。打开面板后直接用 CPAMP 管理员密钥登录，不需要首次 setup。' ;;
    en-US:next_full_stack) printf 'The full Docker install imported the CPA connection into encrypted SQLite storage and configured a demo client API key. Log in with the CPAMP Admin Key; first setup is not required.' ;;
    zh-CN:next_env_managed) printf 'CPA 连接已导入 SQLite，并使用 data.key 进行 AES-GCM 加密。打开面板后直接用 CPAMP 管理员密钥登录；后续轮换请在 Manager 配置页写入新密钥。' ;;
    en-US:next_env_managed) printf 'The CPA connection was imported into SQLite and AES-GCM encrypted with data.key. Log in with the CPAMP Admin Key; rotate the CPA key from Manager configuration.' ;;
    zh-CN:cpa_import_failed) printf 'CPA 连接导入失败；旧运行时配置和临时 secret 已保留。' ;;
    en-US:cpa_import_failed) printf 'CPA connection import failed; the previous runtime config and temporary secret were preserved.' ;;
    zh-CN:cpa_imported) printf 'CPA 连接已导入 SQLite 加密存储' ;;
    en-US:cpa_imported) printf 'CPA connection imported into encrypted SQLite storage' ;;
    zh-CN:cpa_validation_failed) printf 'CPA 连接验证失败；临时密钥已保留，旧版升级将自动回滚。' ;;
    en-US:cpa_validation_failed) printf 'CPA connection validation failed; the temporary key was preserved and legacy upgrades will roll back.' ;;
    zh-CN:cpa_temp_key_file) printf '临时 CPA Management Key 文件（导入成功后删除）' ;;
    en-US:cpa_temp_key_file) printf 'Temporary CPA Management Key file (removed after successful import)' ;;
    zh-CN:cpa_skip_upgrade) printf '旧版 CPA env/secret 迁移仍待执行。检查计划后，请运行下面的完整升级命令；它会负责导入、配置收口、重启、验证和临时密钥清理。' ;;
    en-US:cpa_skip_upgrade) printf 'The legacy CPA env/secret migration is still pending. After reviewing the plan, run the full upgrade command below; it performs import, config cleanup, restart, verification, and temporary-key removal.' ;;
    zh-CN:cpa_rollback_failed) printf 'CPA 连接迁移自动回滚未完全成功；请从 cpa-key-migration 备份恢复配置，并保留临时 secret 后重启服务。' ;;
    en-US:cpa_rollback_failed) printf 'Automatic CPA connection migration rollback did not fully succeed. Restore the cpa-key-migration backups, keep the temporary secret, and restart the service.' ;;
    zh-CN:cpa_key_pending_cleanup) printf '检测到上次安装遗留的临时 CPA Management Key；本次将在健康、管理员和 CPA 代理验证全部通过后删除它。' ;;
    en-US:cpa_key_pending_cleanup) printf 'A temporary CPA Management Key from a previous installer run was detected; it will be removed after health, admin, and CPA proxy validation all pass.' ;;
    zh-CN:cpa_import_retry_loaded) printf '检测到上次失败后保留的 CPA 连接导入状态；本次将自动重试离线导入。' ;;
    en-US:cpa_import_retry_loaded) printf 'A CPA connection import state retained after an earlier failure was detected; the offline import will be retried automatically.' ;;
    zh-CN:cpa_import_state_cleanup_failed) printf '业务迁移已成功，但一次性 CPA 导入状态清理失败；状态和临时密钥均已保留以便安全重试。' ;;
    en-US:cpa_import_state_cleanup_failed) printf 'The migration succeeded, but the one-time CPA import state could not be removed; the state and temporary key were retained for a safe retry.' ;;
    zh-CN:cpa_snapshot_cleanup_failed) printf '迁移已提交，但 Manager 数据快照清理失败；快照已保留，请稍后手动删除。' ;;
    en-US:cpa_snapshot_cleanup_failed) printf 'The migration was committed, but cleaning the Manager data snapshot failed; the snapshot was kept for manual removal.' ;;
    zh-CN:cpa_runtime_backup_cleanup_failed) printf '迁移已提交，但本次 legacy CPA runtime rollback backup 清理失败；文件可能包含旧版 CPA secret，请手动删除。' ;;
    en-US:cpa_runtime_backup_cleanup_failed) printf 'The migration was committed, but a legacy CPA runtime rollback backup could not be removed; it may contain a legacy CPA secret and must be deleted manually.' ;;
    zh-CN:cpa_key_cleanup_failed) printf '业务迁移已成功，但临时 CPA Management Key 清理失败；文件已保留，请稍后手动删除。' ;;
    en-US:cpa_key_cleanup_failed) printf 'The migration succeeded, but cleaning the temporary CPA Management Key failed; the file was retained for manual removal.' ;;
    zh-CN:native_upgrade_pending) printf '旧版 native CPA 配置迁移仍待执行。检查计划后，请运行下面的完整升级命令；旧运行入口和密钥不会在预览阶段被修改。' ;;
    en-US:native_upgrade_pending) printf 'The legacy native CPA configuration migration is still pending. After reviewing the plan, run the full upgrade command below; the existing runtime entry and key are not changed during preview.' ;;
    zh-CN:native_rollback_failed) printf 'Native 升级自动回滚未完全成功；旧运行入口备份和 CPA 密钥均已保留，请人工恢复并启动旧版本。' ;;
    en-US:native_rollback_failed) printf 'Automatic native upgrade rollback did not fully succeed. The previous runtime-entry backup and CPA key were preserved; restore and start the previous version manually.' ;;
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
    zh-CN:select_native_action) printf '检测到已有 native 部署。选择操作: 1) 安全升级并迁移旧 CPA 配置  2) 退出' ;;
    en-US:select_native_action) printf 'An existing native deployment was detected. Select action: 1) safely upgrade and migrate the legacy CPA configuration  2) exit' ;;
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
    if [[ "$line" =~ ^[[:space:]]*(export[[:space:]]+)?${key}[[:space:]]*=(.*)$ ]]; then
      value="${BASH_REMATCH[2]}"
      value="${value#"${value%%[!$' \t']*}"}"
      value="${value%"${value##*[!$' \t']}"}"
      case "$value" in
        \"*)
          [ "${value: -1}" = '"' ] || return 2
          value="${value:1:${#value}-2}"
          ;;
        \'*)
          [ "${value: -1}" = "'" ] || return 2
          value="${value:1:${#value}-2}"
          ;;
      esac
      found="1"
    fi
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
  local has_native_runtime_config="0"
  local native_config=""

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
  if [ -e "$install_dir/run.sh" ] || [ -e "$install_dir/data/usage.sqlite" ]; then
    has_native_files="1"
  fi
  for native_config in "$install_dir"/runtime/*/config.json; do
    if [ -f "$native_config" ]; then
      has_native_runtime_config="1"
      break
    fi
  done

  if [ -f "$install_dir/.env" ] && [ -f "$install_dir/compose.yaml" ]; then
    existing_install_state="managed"
  elif [ "$has_docker_files" = "1" ]; then
    existing_install_state="partial"
  elif [ -f "$install_dir/run.sh" ] && [ "$has_native_runtime_config" = "1" ]; then
    existing_install_state="native-managed"
  elif [ "$has_native_files" = "1" ]; then
    existing_install_state="native-partial"
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

  if [ "$existing_install_state" = "native-managed" ]; then
    say ""
    say "== $(text existing_install) =="
    say "$(text directory_label): $install_dir"
    if [ -z "$operation" ]; then
      if [ "$non_interactive" = "1" ]; then
        die "$(text noninteractive_existing)"
      fi
      choice="$(prompt_choice "$(text select_native_action)" "1" "1 2")"
      case "$choice" in
        1) operation="upgrade" ;;
        2) exit 0 ;;
      esac
    fi
    [ "$operation" = "upgrade" ] || die "CPAMP_OPERATION=$operation is not available for an existing native deployment."
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

  if [ "$existing_install_state" = "partial" ] || [ "$existing_install_state" = "native-partial" ]; then
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
  local restrict_permissions="${2:-1}"
  local value=""
  [ -f "$file" ] || return 1
  # Never chmod through a symlink: the target may be an externally managed
  # secret whose permissions belong to the user, not the installer.
  if [ "$dry_run" != "1" ] && [ "$restrict_permissions" = "1" ] && [ ! -L "$file" ]; then
    chmod 600 "$file" 2>/dev/null || die "Unable to restrict secret file permissions: $file"
  fi
  value="$(< "$file")"
  value="${value%$'\r'}"
  validate_secret_value "$file" "$value"
  printf '%s\n' "$value"
}

materialize_cpa_management_key_file() {
  local file="${cpa_management_key_file:-}"
  local tmp=""
  if [ -z "$file" ] || { [ "$cpa_management_key_inline_input" != "1" ] && [ -f "$file" ]; }; then
    return 0
  fi
  if [ "$dry_run" = "1" ]; then
    return 0
  fi
  [ "$cpa_management_key_cleanup_allowed" = "1" ] ||
    die "CPA Management Key file is missing and is not installer-managed: $file"
  if [ -L "$file" ]; then
    die "Refusing to replace a symlinked CPA Management Key file: $file"
  fi
  [ -n "$cpa_management_key" ] || die "CPA Management Key is empty; refusing to create a temporary key file."
  mkdir -p "$(dirname "$file")"
  tmp="${file}.tmp.$$"
  if ! (umask 077 && printf '%s\n' "$cpa_management_key" > "$tmp"); then
    rm -f "$tmp"
    die "Unable to write temporary CPA Management Key file: $file"
  fi
  chmod 600 "$tmp" || {
    rm -f "$tmp"
    die "Unable to restrict temporary CPA Management Key file permissions: $file"
  }
  mv -f "$tmp" "$file" || {
    rm -f "$tmp"
    die "Unable to publish temporary CPA Management Key file: $file"
  }
}

read_json_string_value() {
  local file="$1"
  local key="$2"
  if command_exists jq; then
    jq -er --arg key "$key" '
      [to_entries[] | select(.key | ascii_downcase == ($key | ascii_downcase))] as $matches
      | if ($matches | length) == 1 and ($matches[0].value | type) == "string"
        then $matches[0].value
        else empty
        end
    ' "$file"
    return
  fi
  if command_exists python3; then
    python3 - "$file" "$key" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    pairs = json.load(handle, object_pairs_hook=lambda value: value)
if not isinstance(pairs, list):
    raise SystemExit(1)
matches = [value for name, value in pairs if name.casefold() == sys.argv[2].casefold()]
if len(matches) != 1 or not isinstance(matches[0], str):
    raise SystemExit(1)
print(matches[0])
PY
    return
  fi
  awk -v key="$(printf '%s' "$key" | tr '[:upper:]' '[:lower:]')" '
    {
      lowered = tolower($0)
    }
    lowered ~ "^[[:space:]]*\\\"" key "\\\"[[:space:]]*:[[:space:]]*\\\"" {
      value = $0
      sub("^[[:space:]]*\\\"[^\\\"]+\\\"[[:space:]]*:[[:space:]]*\\\"", "", value)
      sub("\\\"[[:space:]]*,?[[:space:]]*$", "", value)
      print value
      found = 1
      exit
    }
    END { if (!found) exit 1 }
  ' "$file"
}

json_semantic_key_count() {
  local file="$1"
  local key="$2"
  if command_exists jq; then
    jq -er --arg key "$key" '
      [to_entries[] | select(.key | ascii_downcase == ($key | ascii_downcase))] | length
    ' "$file"
    return
  fi
  if command_exists python3; then
    python3 - "$file" "$key" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    pairs = json.load(handle, object_pairs_hook=lambda value: value)
if not isinstance(pairs, list):
    raise SystemExit(1)
print(sum(1 for name, _ in pairs if name.casefold() == sys.argv[2].casefold()))
PY
    return
  fi
  grep -oiE "\"$key\"[[:space:]]*:" "$file" | wc -l | tr -d '[:space:]'
}

require_json_string_value() {
  local file="$1"
  local key="$2"
  local count=""
  local value=""
  count="$(json_semantic_key_count "$file" "$key" 2>/dev/null)" ||
    die "Existing native config is not valid JSON."
  [ "$count" = "1" ] ||
    die "Existing native config must declare exactly one case-insensitive $key field."
  value="$(read_json_string_value "$file" "$key" 2>/dev/null)" ||
    die "Existing native config does not declare $key as a supported string value."
  printf '%s\n' "$value"
}

json_key_declared() {
  local file="$1"
  local key="$2"
  local count=""
  count="$(json_semantic_key_count "$file" "$key" 2>/dev/null)" || return 1
  [ "$count" -gt 0 ]
}

resolve_native_config_path() {
  local value="$1"
  local config_dir="$2"
  local candidate=""
  local parent=""

  validate_single_line "Native config path" "$value"
  case "$value" in
    *\\*) die "Legacy native config paths containing JSON escapes are not supported: $value" ;;
    /*) candidate="$value" ;;
    *) candidate="$config_dir/$value" ;;
  esac
  parent="$(cd "$(dirname "$candidate")" 2>/dev/null && pwd -P)" ||
    die "Native config path parent does not exist: $candidate"
  printf '%s/%s\n' "$parent" "$(basename "$candidate")"
}

decode_native_entry_value() {
  local value="$1"
  local source="$2"
  local result=""
  local char=""
  local index=0

  value="${value#${value%%[!$' \t']*}}"
  value="${value%${value##*[!$' \t']}}"
  case "$value" in
    \"*)
      [ "${value: -1}" = '"' ] || return 1
      value="${value:1:${#value}-2}"
      ;;
    \'*)
      [ "${value: -1}" = "'" ] || return 1
      value="${value:1:${#value}-2}"
      ;;
  esac

  while [ "$index" -lt "${#value}" ]; do
    char="${value:index:1}"
    if [ "$char" = "\\" ]; then
      index=$((index + 1))
      [ "$index" -lt "${#value}" ] || return 1
      result+="${value:index:1}"
    else
      result+="$char"
    fi
    index=$((index + 1))
  done
  if [ "$source" = "systemd" ]; then
    result="${result//%%/%}"
  fi
  printf '%s\n' "$result"
}

record_native_entry_path() {
  local source="$1"
  local variable="$2"
  local value="$3"
  local declared="0"
  local current=""

  case "$source:$variable" in
    run:USAGE_DATA_DIR)
      declared="$native_run_data_dir_declared"
      current="$native_run_data_dir"
      ;;
    run:USAGE_DB_PATH)
      declared="$native_run_db_path_declared"
      current="$native_run_db_path"
      ;;
    run:CPA_MANAGER_DATA_KEY_PATH)
      declared="$native_run_data_key_path_declared"
      current="$native_run_data_key_path"
      ;;
    systemd:USAGE_DATA_DIR)
      declared="$native_service_data_dir_declared"
      current="$native_service_data_dir"
      ;;
    systemd:USAGE_DB_PATH)
      declared="$native_service_db_path_declared"
      current="$native_service_db_path"
      ;;
    systemd:CPA_MANAGER_DATA_KEY_PATH)
      declared="$native_service_data_key_path_declared"
      current="$native_service_data_key_path"
      ;;
    *) die "Unsupported native runtime path variable: $variable" ;;
  esac

  if [ "$declared" = "1" ]; then
    [ "$current" = "$value" ] ||
      die "Native $source runtime declares conflicting $variable values."
    return
  fi

  case "$source:$variable" in
    run:USAGE_DATA_DIR)
      native_run_data_dir="$value"
      native_run_data_dir_declared="1"
      ;;
    run:USAGE_DB_PATH)
      native_run_db_path="$value"
      native_run_db_path_declared="1"
      ;;
    run:CPA_MANAGER_DATA_KEY_PATH)
      native_run_data_key_path="$value"
      native_run_data_key_path_declared="1"
      ;;
    systemd:USAGE_DATA_DIR)
      native_service_data_dir="$value"
      native_service_data_dir_declared="1"
      ;;
    systemd:USAGE_DB_PATH)
      native_service_db_path="$value"
      native_service_db_path_declared="1"
      ;;
    systemd:CPA_MANAGER_DATA_KEY_PATH)
      native_service_data_key_path="$value"
      native_service_data_key_path_declared="1"
      ;;
  esac
}

normalize_native_entry_path() {
  local source="$1"
  local variable="$2"
  local raw_value="$3"
  local value=""

  value="$(decode_native_entry_value "$raw_value" "$source")" ||
    die "Unable to parse $variable from the native $source runtime entry."
  validate_single_line "$variable" "$value"
  if [ -n "$value" ]; then
    case "$value" in
      /*) ;;
      *) die "$variable in the native $source runtime entry must be an absolute path." ;;
    esac
    case "$value" in
      */../*|*/..|*/./*|*/.)
        die "$variable in the native $source runtime entry contains an unsupported relative path segment."
        ;;
    esac
    value="$(resolve_native_config_path "$value" "$native_existing_config_dir")"
  fi
  record_native_entry_path "$source" "$variable" "$value"
}

parse_native_run_paths() {
  local line=""
  local variable=""
  local raw_value=""

  native_run_data_dir=""
  native_run_db_path=""
  native_run_data_key_path=""
  native_run_data_dir_declared="0"
  native_run_db_path_declared="0"
  native_run_data_key_path_declared="0"
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      [[:space:]]*\#*) continue ;;
    esac
    if [[ "$line" =~ ^[[:space:]]*(export[[:space:]]+)?(USAGE_DATA_DIR|USAGE_DB_PATH|CPA_MANAGER_DATA_KEY_PATH)[[:space:]]*=(.*)$ ]]; then
      variable="${BASH_REMATCH[2]}"
      raw_value="${BASH_REMATCH[3]}"
      normalize_native_entry_path run "$variable" "$raw_value"
      continue
    fi
    if [[ "$line" == *USAGE_DATA_DIR* || "$line" == *USAGE_DB_PATH* || "$line" == *CPA_MANAGER_DATA_KEY_PATH* ]]; then
      if [[ "$line" == *"unset USAGE_DATA_DIR"* ||
            "$line" == *"unset USAGE_DB_PATH"* ||
            "$line" == *"unset CPA_MANAGER_DATA_KEY_PATH"* ]]; then
        continue
      fi
      die "Unable to determine the effective Manager data path from the native run.sh entry."
    fi
  done < "$install_dir/run.sh"
}

parse_native_systemd_paths() {
  local service_file="$install_dir/cpa-manager-plus.service"
  local line=""
  local raw_value=""
  local variable=""
  local value=""

  native_service_data_dir=""
  native_service_db_path=""
  native_service_data_key_path=""
  native_service_data_dir_declared="0"
  native_service_db_path_declared="0"
  native_service_data_key_path_declared="0"
  [ -f "$service_file" ] || return 0
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      [[:space:]]*\#*|'') continue ;;
    esac
    if [[ "$line" =~ ^[[:space:]]*EnvironmentFile= ]]; then
      die "The existing native systemd service uses EnvironmentFile; refusing to guess Manager data paths."
    fi
    if [[ "$line" =~ ^[[:space:]]*Environment=(.*)$ ]]; then
      raw_value="${BASH_REMATCH[1]}"
      value="$(decode_native_entry_value "$raw_value" systemd)" ||
        die "Unable to parse an Environment entry from the existing native systemd service."
      if [[ "$value" =~ ^(USAGE_DATA_DIR|USAGE_DB_PATH|CPA_MANAGER_DATA_KEY_PATH)=(.*)$ ]]; then
        variable="${BASH_REMATCH[1]}"
        raw_value="${BASH_REMATCH[2]}"
        normalize_native_entry_path systemd "$variable" "$raw_value"
      elif [[ "$value" == *USAGE_DATA_DIR* || "$value" == *USAGE_DB_PATH* || "$value" == *CPA_MANAGER_DATA_KEY_PATH* ]]; then
        die "Unable to determine the effective Manager data path from the native systemd service."
      fi
      continue
    fi
    if [[ "$line" == *USAGE_DATA_DIR* || "$line" == *USAGE_DB_PATH* || "$line" == *CPA_MANAGER_DATA_KEY_PATH* ]]; then
      die "Unable to determine the effective Manager data path from the native systemd service."
    fi
  done < "$service_file"
}

compute_native_entry_paths() {
  local source="$1"
  local data_dir="$native_config_data_dir"
  local db_path="$native_config_db_path"
  local data_key_path="$native_config_data_key_path"
  local data_declared="0"
  local db_declared="0"
  local data_key_declared="0"
  local data_value=""
  local db_value=""
  local data_key_value=""

  case "$source" in
    run)
      data_declared="$native_run_data_dir_declared"
      db_declared="$native_run_db_path_declared"
      data_key_declared="$native_run_data_key_path_declared"
      data_value="$native_run_data_dir"
      db_value="$native_run_db_path"
      data_key_value="$native_run_data_key_path"
      ;;
    systemd)
      data_declared="$native_service_data_dir_declared"
      db_declared="$native_service_db_path_declared"
      data_key_declared="$native_service_data_key_path_declared"
      data_value="$native_service_data_dir"
      db_value="$native_service_db_path"
      data_key_value="$native_service_data_key_path"
      ;;
    config) ;;
    *) die "Unsupported native runtime path source: $source" ;;
  esac

  if [ "$data_declared" = "1" ] && [ -n "$data_value" ]; then
    data_dir="$data_value"
  fi
  if [ "$db_declared" = "1" ] && [ -n "$db_value" ]; then
    db_path="$db_value"
  elif [ "$data_declared" = "1" ] && [ -n "$data_value" ]; then
    db_path="$data_dir/usage.sqlite"
  fi
  if [ "$data_key_declared" = "1" ] && [ -n "$data_key_value" ]; then
    data_key_path="$data_key_value"
  elif [ "$data_declared" = "1" ] && [ -n "$data_value" ] && [ "$native_config_data_key_declared" != "1" ]; then
    data_key_path="$data_dir/data.key"
  fi

  native_effective_data_dir="$data_dir"
  native_effective_db_path="$db_path"
  native_effective_data_key_path="$data_key_path"
}

resolve_native_runtime_paths() {
  local run_data_dir=""
  local run_db_path=""
  local run_data_key_path=""
  local service_data_dir=""
  local service_db_path=""
  local service_data_key_path=""
  local run_present="0"
  local service_present="0"

  parse_native_run_paths
  if [ -f "$install_dir/cpa-manager-plus.service" ]; then
    service_present="1"
    parse_native_systemd_paths
  fi

  if [ -f "$install_dir/run.sh" ]; then
    run_present="1"
    compute_native_entry_paths run
    run_data_dir="$native_effective_data_dir"
    run_db_path="$native_effective_db_path"
    run_data_key_path="$native_effective_data_key_path"
  fi
  if [ "$service_present" = "1" ]; then
    compute_native_entry_paths systemd
    service_data_dir="$native_effective_data_dir"
    service_db_path="$native_effective_db_path"
    service_data_key_path="$native_effective_data_key_path"
  fi

  if [ "$run_present" = "1" ] && [ "$service_present" = "1" ]; then
    [ "$run_data_dir" = "$service_data_dir" ] &&
      [ "$run_db_path" = "$service_db_path" ] &&
      [ "$run_data_key_path" = "$service_data_key_path" ] ||
      die "Native run.sh and systemd service resolve different Manager data paths."
    native_data_dir="$run_data_dir"
    native_db_path="$run_db_path"
    native_data_key_path="$run_data_key_path"
  elif [ "$run_present" = "1" ]; then
    native_data_dir="$run_data_dir"
    native_db_path="$run_db_path"
    native_data_key_path="$run_data_key_path"
  elif [ "$service_present" = "1" ]; then
    native_data_dir="$service_data_dir"
    native_db_path="$service_db_path"
    native_data_key_path="$service_data_key_path"
  else
    native_data_dir="$native_config_data_dir"
    native_db_path="$native_config_db_path"
    native_data_key_path="$native_config_data_key_path"
  fi
}

require_native_manager_data_files() {
  local label=""
  local file=""
  for label in "Manager database" "Manager data key"; do
    if [ "$label" = "Manager database" ]; then
      file="$native_db_path"
    else
      file="$native_data_key_path"
    fi
    [ -f "$file" ] || die "$label is missing or is not a regular file: $file"
    [ -r "$file" ] || die "$label is not readable: $file"
  done
}

path_is_managed_cpa_key_file() {
  local candidate="$1"
  local secrets_dir=""
  secrets_dir="$(cd "$install_dir/secrets" 2>/dev/null && pwd -P)" || return 1
  [ "$candidate" = "$secrets_dir/cpa-management-key" ]
}

cpa_management_key_external_marker_path() {
  local secrets_dir=""
  secrets_dir="$(cd "$install_dir/secrets" 2>/dev/null && pwd -P)" || return 1
  printf '%s\n' "$secrets_dir/.cpa-management-key.external"
}

mark_cpa_management_key_external() {
  local marker=""
  local tmp=""
  marker="$(cpa_management_key_external_marker_path)" || return 1
  # An existing marker, including a symlink, is already a safe refusal to
  # classify the canonical key as installer-owned. Never follow or replace it.
  if [ -e "$marker" ] || [ -L "$marker" ]; then
    return 0
  fi
  if [ "$dry_run" = "1" ]; then
    return 0
  fi
  tmp="${marker}.tmp.$$"
  if ! (umask 077 && printf 'EXTERNAL=1\n' > "$tmp"); then
    rm -f "$tmp"
    return 1
  fi
  chmod 600 "$tmp" || {
    rm -f "$tmp"
    return 1
  }
  if ! mv -f "$tmp" "$marker"; then
    rm -f "$tmp"
    return 1
  fi
}

canonical_parent_path() {
  local candidate="$1"
  local parent=""
  parent="$(cd "$(dirname "$candidate")" 2>/dev/null && pwd -P)" || return 1
  printf '%s/%s\n' "$parent" "$(basename "$candidate")"
}

# is_installer_owned_cpa_key_file is the single ownership predicate for CPA
# Management Key files: only a regular file (final path component is not a
# symlink) at the canonical managed secrets path counts as installer-owned and
# may be chmod-ed, marked cleanup-eligible, or removed by the installer. A
# symlink at the managed path is treated as an externally managed secret: its
# content may be read as migration input, but the symlink, its target, and the
# target's permissions must never be touched. A regular file supplied through
# an external CPA_MANAGEMENT_KEY_FILE is recorded with a small ownership marker
# before the runtime reference is removed, so a later rerun cannot mistake it
# for an installer-created leftover.
is_installer_owned_cpa_key_file() {
  local candidate="$1"
  local canonical=""
  local marker=""
  [ -n "$candidate" ] || return 1
  [ -f "$candidate" ] || return 1
  if [ -L "$candidate" ]; then
    return 1
  fi
  canonical="$(canonical_parent_path "$candidate")" || return 1
  path_is_managed_cpa_key_file "$canonical" || return 1
  marker="$(cpa_management_key_external_marker_path)" || return 1
  [ ! -e "$marker" ] && [ ! -L "$marker" ]
}

is_installer_owned_cpa_import_input() {
  local candidate="$1"
  local canonical=""
  local secrets_dir=""
  local marker=""
  local name=""
  [ -n "$candidate" ] && [ -f "$candidate" ] && [ ! -L "$candidate" ] || return 1
  canonical="$(canonical_parent_path "$candidate")" || return 1
  secrets_dir="$(cd "$install_dir/secrets" 2>/dev/null && pwd -P)" || return 1
  [ "$(dirname "$canonical")" = "$secrets_dir" ] || return 1
  name="$(basename "$canonical")"
  case "$name" in
    cpa-management-key)
      marker="$(cpa_management_key_external_marker_path)" || return 1
      [ ! -e "$marker" ] && [ ! -L "$marker" ]
      ;;
    cpa-management-key.import.*)
      name="${name#cpa-management-key.import.}"
      case "$name" in
        ''|*[!0-9]*) return 1 ;;
        *) return 0 ;;
      esac
      ;;
    *) return 1 ;;
  esac
}

cpa_import_retry_path() {
  printf '%s\n' "$install_dir/secrets/cpa-connection-import.pending"
}

normalize_cpa_url_value() {
  local value="$1"
  value="${value#"${value%%[!$' \t']*}"}"
  value="${value%"${value##*[!$' \t']}"}"
  while [[ "$value" == */ ]]; do value="${value%/}"; done
  case "$value" in
    */v0/management) value="${value%/v0/management}" ;;
    */v0) value="${value%/v0}" ;;
  esac
  printf '%s\n' "$value"
}

load_cpa_import_retry_state() {
  local state_file=""
  local version=""
  local key_file_name=""
  local key_ownership=""
  local declared_lines=""
  local candidate=""

  state_file="$(cpa_import_retry_path)"
  if [ ! -e "$state_file" ] && [ ! -L "$state_file" ]; then
    return 1
  fi
  [ -f "$state_file" ] && [ ! -L "$state_file" ] ||
    die "CPA import retry state is not an installer-owned regular file: $state_file"
  if [ "$dry_run" != "1" ]; then
    chmod 600 "$state_file" || die "Unable to restrict CPA import retry state permissions: $state_file"
  fi
  declared_lines="$(awk '
    /^[[:space:]]*$/ { next }
    /^VERSION=/ { version++ ; next }
    /^CPA_URL=/ { url++ ; next }
    /^KEY_FILE=/ { key_file++ ; next }
    /^KEY_OWNERSHIP=/ { ownership++ ; next }
    { invalid++ }
    END {
      if (version == 1 && url == 1 && key_file == 1 && ownership == 1 && invalid == 0) print "valid"
    }
  ' "$state_file")"
  [ "$declared_lines" = "valid" ] || die "CPA import retry state is invalid: $state_file"

  version="$(read_env_value "$state_file" VERSION)" || die "CPA import retry state has no version."
  cpa_url="$(read_env_value "$state_file" CPA_URL)" || die "CPA import retry state has no CPA URL."
  key_file_name="$(read_env_value "$state_file" KEY_FILE)" || die "CPA import retry state has no key file."
  key_ownership="$(read_env_value "$state_file" KEY_OWNERSHIP)" || die "CPA import retry state has no key ownership."
  [ "$version" = "1" ] || die "Unsupported CPA import retry state version: $version"
  [ "$key_ownership" = "installer" ] || die "Unsupported CPA import key ownership: $key_ownership"
  case "$key_file_name" in
    cpa-management-key) ;;
    cpa-management-key.import.*)
      case "${key_file_name#cpa-management-key.import.}" in
        ''|*[!0-9]*) die "CPA import retry state contains an unsafe key file name: $key_file_name" ;;
      esac
      ;;
    *) die "CPA import retry state contains an unsafe key file name: $key_file_name" ;;
  esac
  validate_url_value "$(text cpa_url)" "$cpa_url"
  candidate="$install_dir/secrets/$key_file_name"
  is_installer_owned_cpa_import_input "$candidate" ||
    die "CPA import retry key is missing or is not installer-owned: $candidate"
  cpa_management_key_file="$(canonical_parent_path "$candidate")" ||
    die "Unable to resolve CPA import retry key: $candidate"
  cpa_management_key="$(read_existing_secret "$cpa_management_key_file" "1")" ||
    die "CPA import retry key is not readable: $cpa_management_key_file"
  cpa_management_key_cleanup_allowed="1"
  case "$key_file_name" in
    cpa-management-key.import.*) cpa_management_key_import_copy="1" ;;
  esac
  cpa_connection_mode="env"
  cpa_import_retry_state_required="1"
  cpa_import_retry_state_loaded="1"
  cpa_import_retry_state_file="$state_file"
  return 0
}

load_and_validate_cpa_import_retry_state() {
  local runtime_url="$cpa_url"
  local runtime_key="$cpa_management_key"

  load_cpa_import_retry_state ||
    die "CPA import retry state could not be loaded for the existing runtime configuration."
  if [ "$(normalize_cpa_url_value "$runtime_url")" != "$(normalize_cpa_url_value "$cpa_url")" ] ||
     [ "$runtime_key" != "$cpa_management_key" ]; then
    die "Existing runtime CPA connection does not match the retained CPA import retry state; refusing to choose between migration inputs."
  fi
}

load_explicit_cpa_import_recovery() {
  local requested_mode="${CPAMP_CPA_CONNECTION_MODE:-}"
  local managed_key="$install_dir/secrets/cpa-management-key"
  local existing_key=""

  [ -n "$requested_mode" ] || return 1
  case "$requested_mode" in
    env|secret|managed) ;;
    setup|panel|later) return 1 ;;
    *) die "Unsupported CPAMP_CPA_CONNECTION_MODE: $requested_mode" ;;
  esac
  [ -n "${CPAMP_CPA_URL:-}" ] ||
    die "CPAMP_CPA_URL is required to recover an unfinished CPA connection import."
  cpa_url="$CPAMP_CPA_URL"
  validate_url_value "$(text cpa_url)" "$cpa_url"

  cpa_management_key_file="$managed_key"
  if is_installer_owned_cpa_key_file "$managed_key"; then
    existing_key="$(read_existing_secret "$managed_key" "1")" ||
      die "CPA Management Key file is not readable: $managed_key"
    if [ -n "${CPAMP_CPA_MANAGEMENT_KEY:-}" ] && [ "$CPAMP_CPA_MANAGEMENT_KEY" != "$existing_key" ]; then
      die "CPAMP_CPA_MANAGEMENT_KEY conflicts with the retained installer-managed key."
    fi
    cpa_management_key="$existing_key"
  else
    [ ! -e "$managed_key" ] && [ ! -L "$managed_key" ] ||
      die "The managed CPA key path is externally owned; recovery requires restoring the original runtime reference."
    [ -n "${CPAMP_CPA_MANAGEMENT_KEY:-}" ] ||
      die "CPAMP_CPA_MANAGEMENT_KEY is required because no retained installer-managed key exists."
    cpa_management_key="$CPAMP_CPA_MANAGEMENT_KEY"
    validate_secret_value "$(text cpa_key)" "$cpa_management_key"
    cpa_management_key_inline_input="1"
  fi
  cpa_management_key_cleanup_allowed="1"
  cpa_connection_mode="env"
  cpa_import_retry_state_required="1"
  return 0
}

persist_cpa_import_retry_state() {
  local state_file=""
  local tmp=""
  local key_file_name=""

  [ "$cpa_import_retry_state_required" = "1" ] || return 0
  # External CPA_MANAGEMENT_KEY_FILE inputs remain owned by the operator. They
  # do not need installer retry state because they are never eligible for
  # cleanup; persisting them as KEY_OWNERSHIP=installer would let a later retry
  # mistake a canonical external file for a temporary installer secret.
  if [ "$cpa_management_key_cleanup_allowed" != "1" ]; then
    cpa_import_retry_state_required="0"
    return 0
  fi
  is_installer_owned_cpa_import_input "$cpa_management_key_file" ||
    die "Refusing to persist retry state for a CPA key that is not installer-owned: $cpa_management_key_file"
  key_file_name="$(basename "$cpa_management_key_file")"
  state_file="$(cpa_import_retry_path)"
  tmp="${state_file}.tmp.$$"
  if ! (umask 077 && {
    printf 'VERSION=1\n'
    printf 'CPA_URL=%s\n' "$cpa_url"
    printf 'KEY_FILE=%s\n' "$key_file_name"
    printf 'KEY_OWNERSHIP=installer\n'
  } > "$tmp"); then
    rm -f "$tmp"
    die "Unable to write CPA import retry state: $state_file"
  fi
  chmod 600 "$tmp" || {
    rm -f "$tmp"
    die "Unable to restrict CPA import retry state permissions: $state_file"
  }
  mv -f "$tmp" "$state_file" || {
    rm -f "$tmp"
    die "Unable to publish CPA import retry state: $state_file"
  }
  cpa_import_retry_state_file="$state_file"
}

# detect_installer_managed_cpa_key_pending_cleanup recognizes an unfinished
# CPA connection finalization from an earlier installer run: the
# installer-managed plaintext key file still exists while the runtime config no
# longer references any CPA key input. Only an installer-owned regular file at
# the managed path qualifies (is_installer_owned_cpa_key_file): external
# CPA_MANAGEMENT_KEY_FILE values never point here, and symlinks are externally
# managed so nothing outside the install directory can be removed. Import
# copies (cpa-management-key.import.<pid>...) left behind by a failed inline
# import are recorded separately and removed only after this run re-verifies
# the stored connection through the CPA proxy.
detect_installer_managed_cpa_key_pending_cleanup() {
  local managed_key="$install_dir/secrets/cpa-management-key"
  local stale_entry=""
  for stale_entry in "$install_dir"/secrets/cpa-management-key.import.[0-9]*; do
    if [ -f "$stale_entry" ] && [ ! -L "$stale_entry" ]; then
      installer_managed_cpa_key_pending_import_copies+="${stale_entry}"$'\n'
    fi
  done
  if [ "$cpa_connection_mode" = "env" ]; then
    return 0
  fi
  if ! is_installer_owned_cpa_key_file "$managed_key"; then
    return 0
  fi
  # Keep the canonical physical form (for example /var versus /private/var)
  # for the recorded cleanup target, exactly like the runtime config readers.
  managed_key="$(canonical_parent_path "$managed_key")" || return 0
  installer_managed_cpa_key_pending_cleanup="1"
  installer_managed_cpa_key_pending_file="$managed_key"
  return 0
}

compose_service_references_token() {
  local token="$1"
  awk '
    function indentation(value) {
      match(value, /^[[:space:]]*/)
      return RLENGTH
    }
    BEGIN { in_services = 0; in_service = 0; services_indent = -1; service_indent = -1 }
    {
      line = $0
      indent = indentation(line)
      if (in_service && line !~ /^[[:space:]]*$/ && indent <= service_indent) {
        in_service = 0
      }
      if (line ~ /^[[:space:]]*services:[[:space:]]*$/) {
        in_services = 1
        services_indent = indent
      } else if (in_services && line !~ /^[[:space:]]*$/ && indent <= services_indent) {
        in_services = 0
        in_service = 0
      }
      if (in_services && line ~ /^[[:space:]]*["\047]?cpa-manager-plus["\047]?:[[:space:]]*$/) {
        in_service = 1
        service_indent = indent
        next
      }
      if (in_service && line !~ /^[[:space:]]*#/ && line ~ ("(^|[^A-Za-z0-9_])" token "([^A-Za-z0-9_]|$)")) {
        found = 1
      }
    }
    END { exit(found ? 0 : 1) }
  ' token="$token" "$install_dir/compose.yaml"
}

# load_docker_compose_config uses Docker Compose's resolved configuration when
# it is available. The raw file remains the fallback for dry-runs and older or
# test Compose shims; the small parser below only handles the two environment
# forms and volume forms supported by the installer contract.
load_docker_compose_config() {
  local rendered=""
  local status=0
  if [ "$docker_compose_config_loaded" = "1" ]; then
    return 0
  fi
  [ -f "$install_dir/compose.yaml" ] || return 1
  if [ "$dry_run" != "1" ] && [ "$skip_execute" != "1" ] && command_exists docker; then
    if rendered="$(cd "$install_dir" && docker compose config 2>/dev/null)"; then
      if [ -n "$rendered" ]; then
        docker_compose_config_source="$rendered"
      fi
    else
      status=$?
      printf '%s\n' "Unable to resolve the existing Docker Compose configuration (exit status $status)." >&2
      return 2
    fi
  fi
  if [ -z "$docker_compose_config_source" ]; then
    docker_compose_config_source="$(< "$install_dir/compose.yaml")"
  fi
  docker_compose_config_loaded="1"
}

compose_variable_value() {
  local variable_name="$1"
  local value=""
  local status=0
  if [ "${!variable_name+x}" = "x" ]; then
    printf '%s' "${!variable_name}"
    return 0
  fi
  if value="$(read_env_value "$install_dir/.env" "$variable_name" 2>/dev/null)"; then
    printf '%s' "$value"
    return 0
  else
    status=$?
    if [ "$status" -eq 1 ]; then
      return 1
    fi
    return "$status"
  fi
}

interpolate_docker_compose_value() {
  local value="$1"
  local prefix=""
  local suffix=""
  local variable_name=""
  local operator=""
  local fallback=""
  local replacement=""
  local variable_set="0"
  local iterations=0
  local status=0

  while [[ "$value" =~ ^(.*)\$\{([A-Za-z_][A-Za-z0-9_]*)(:-|-)?([^}]*)\}(.*)$ ]]; do
    iterations=$((iterations + 1))
    if [ "$iterations" -gt 32 ]; then
      printf '%s\n' "Docker Compose path interpolation is too deeply nested." >&2
      return 2
    fi
    prefix="${BASH_REMATCH[1]}"
    variable_name="${BASH_REMATCH[2]}"
    operator="${BASH_REMATCH[3]}"
    fallback="${BASH_REMATCH[4]}"
    suffix="${BASH_REMATCH[5]}"
    if replacement="$(compose_variable_value "$variable_name")"; then
      variable_set="1"
    else
      status=$?
      if [ "$status" -ne 1 ]; then
        printf '%s\n' "Unable to read Docker Compose interpolation variable $variable_name." >&2
        return "$status"
      fi
      replacement=""
      variable_set="0"
    fi
    if [ -z "$operator" ] && [ -n "$fallback" ]; then
      printf '%s\n' "Unsupported Docker Compose interpolation syntax for $variable_name." >&2
      return 2
    fi
    if [ "$operator" = ":-" ] && [ -z "$replacement" ]; then
      replacement="$fallback"
    elif [ "$operator" = "-" ] && [ "$variable_set" = "0" ]; then
      replacement="$fallback"
    fi
    value="${prefix}${replacement}${suffix}"
  done
  if [[ "$value" == *'${'* ]]; then
    printf '%s\n' "Unsupported or incomplete Docker Compose path interpolation." >&2
    return 2
  fi
  value="${value//\$\$/$}"
  printf '%s\n' "$value"
}

compose_service_environment_value() {
  local key="$1"
  local value=""
  local status=0
  load_docker_compose_config || return $?
  value="$(printf '%s\n' "$docker_compose_config_source" | awk -v wanted_key="$key" '
    function indentation(value) {
      match(value, /^[[:space:]]*/)
      return RLENGTH
    }
    function trim(value) {
      sub(/^[[:space:]]*/, "", value)
      sub(/[[:space:]]*$/, "", value)
      return value
    }
    function clean(value) {
      value = trim(value)
      # docker compose config renders YAML null values as the unquoted scalar
      # `null` (and some Compose versions preserve `~`). Both mean that the
      # service environment value is empty, so the Manager Server should use
      # its own default path. Quoted "null"/"~" remain literal strings.
      if (value == "null" || value == "~") return ""
      if (value ~ /^".*"$/ || value ~ /^\047.*\047$/) {
        value = substr(value, 2, length(value) - 2)
      }
      return value
    }
    function mapping_name(value, position) {
      position = index(value, ":")
      if (position == 0) return ""
      return clean(substr(value, 1, position - 1))
    }
    function mapping_value(value, position) {
      position = index(value, ":")
      if (position == 0) return ""
      return clean(substr(value, position + 1))
    }
    BEGIN {
      in_services = 0
      in_service = 0
      in_environment = 0
      services_indent = -1
      service_indent = -1
      environment_indent = -1
      found = 0
      result = ""
    }
    {
      line = $0
      indent = indentation(line)
      if (in_environment && line !~ /^[[:space:]]*$/ && indent <= environment_indent) {
        in_environment = 0
      }
      if (in_service && line !~ /^[[:space:]]*$/ && indent <= service_indent) {
        in_service = 0
        in_environment = 0
      }
      if (line ~ /^[[:space:]]*services:[[:space:]]*$/) {
        in_services = 1
        services_indent = indent
        next
      }
      if (in_services && line !~ /^[[:space:]]*$/ && indent <= services_indent) {
        in_services = 0
        in_service = 0
        in_environment = 0
      }
      if (in_services && line ~ /^[[:space:]]*["\047]?cpa-manager-plus["\047]?:[[:space:]]*$/) {
        in_service = 1
        service_indent = indent
        next
      }
      if (!in_service) next
      if (!in_environment && line ~ /^[[:space:]]*environment:[[:space:]]*$/) {
        in_environment = 1
        environment_indent = indent
        next
      }
      if (!in_environment || line ~ /^[[:space:]]*$/ || line ~ /^[[:space:]]*#/) next
      if (indent <= environment_indent) next

      candidate = line
      sub(/^[[:space:]]*/, "", candidate)
      if (substr(candidate, 1, 1) == "-") {
        sub(/^-[[:space:]]*/, "", candidate)
        candidate = clean(candidate)
        equals = index(candidate, "=")
        if (equals > 0) {
          name = clean(substr(candidate, 1, equals - 1))
          value = substr(candidate, equals + 1)
        } else {
          name = candidate
          value = "__CPAMP_COMPOSE_PASSTHROUGH__"
        }
      } else {
        name = mapping_name(candidate)
        value = mapping_value(candidate)
      }
      if (name == wanted_key) {
        found++
        result = clean(value)
      }
    }
    END {
      if (found > 1) exit 2
      if (found == 1) {
        print result
        exit 0
      }
      exit 1
    }
  ')" || status=$?
  case "$status" in
    0)
      if [ "$value" = "__CPAMP_COMPOSE_PASSTHROUGH__" ]; then
        if value="$(compose_variable_value "$key")"; then
          [ -n "$value" ] || return 0
          printf '%s\n' "$value"
          return 0
        else
          status=$?
          [ "$status" -eq 1 ] && return 1
          return "$status"
        fi
      fi
      interpolate_docker_compose_value "$value"
      return $?
      ;;
    1) return 1 ;;
    *)
      if [ "$status" -eq 2 ]; then
        printf '%s\n' "Existing Docker Compose contains duplicate $key environment entries." >&2
      fi
      return "$status"
      ;;
  esac
}

docker_compose_volume_targets() {
  load_docker_compose_config || return 0
  printf '%s\n' "$docker_compose_config_source" | awk '
    function indentation(value) {
      match(value, /^[[:space:]]*/)
      return RLENGTH
    }
    function trim(value) {
      sub(/^[[:space:]]*/, "", value)
      sub(/[[:space:]]*$/, "", value)
      return value
    }
    function clean(value) {
      value = trim(value)
      if (value ~ /^".*"$/ || value ~ /^\047.*\047$/) {
        value = substr(value, 2, length(value) - 2)
      }
      return value
    }
    function short_target(value, parts, count) {
      value = clean(value)
      count = split(value, parts, ":")
      if (count < 2) return ""
      if (parts[2] ~ /^\//) return parts[2]
      if (count >= 3 && parts[3] ~ /^\//) return parts[3]
      return ""
    }
    BEGIN {
      in_services = 0
      in_service = 0
      in_volumes = 0
      services_indent = -1
      service_indent = -1
      volumes_indent = -1
    }
    {
      line = $0
      indent = indentation(line)
      if (in_volumes && line !~ /^[[:space:]]*$/ && indent <= volumes_indent) {
        in_volumes = 0
      }
      if (in_service && line !~ /^[[:space:]]*$/ && indent <= service_indent) {
        in_service = 0
        in_volumes = 0
      }
      if (line ~ /^[[:space:]]*services:[[:space:]]*$/) {
        in_services = 1
        services_indent = indent
        next
      }
      if (in_services && line !~ /^[[:space:]]*$/ && indent <= services_indent) {
        in_services = 0
        in_service = 0
        in_volumes = 0
      }
      if (in_services && line ~ /^[[:space:]]*["\047]?cpa-manager-plus["\047]?:[[:space:]]*$/) {
        in_service = 1
        service_indent = indent
        next
      }
      if (!in_service) next
      if (line ~ /^[[:space:]]*volumes:[[:space:]]*$/) {
        in_volumes = 1
        volumes_indent = indent
        next
      }
      if (!in_volumes || line ~ /^[[:space:]]*$/ || line ~ /^[[:space:]]*#/) next
      if (indent <= volumes_indent) next
      candidate = line
      sub(/^[[:space:]]*/, "", candidate)
      if (substr(candidate, 1, 1) == "-") {
        sub(/^-[[:space:]]*/, "", candidate)
        target = short_target(candidate)
        if (target != "") print target
        next
      }
      name = candidate
      position = index(name, ":")
      if (position > 0 && clean(substr(name, 1, position - 1)) == "target") {
        target = clean(substr(name, position + 1))
        if (target ~ /^\//) print target
      }
    }
  '
}

docker_path_is_under_mount() {
  local path="$1"
  local mount="$2"
  case "$path" in
    /) return 0 ;;
    "$mount") return 0 ;;
    "$mount"/*) return 0 ;;
    *) return 1 ;;
  esac
}

validate_docker_container_path() {
  local label="$1"
  local value="$2"
  validate_single_line "$label" "$value"
  case "$value" in
    /*) ;;
    *) die "$label must be an absolute container path: $value" ;;
  esac
  case "$value" in
    */../*|*/..|*/./*|*/.) die "$label contains an unsupported relative path segment: $value" ;;
  esac
}

# resolve_docker_container_paths sets the paths used by every Docker database
# mutation. Compose itself is asked for the resolved service config first, so
# mapping/list environment syntax and .env interpolation follow the running
# deployment. The static volume check is intentionally limited to custom paths;
# legacy files without a volume declaration may still use the default /data
# path, while a custom path must prove that it is mounted.
resolve_docker_container_paths() {
  local value=""
  local mount_targets=""
  local target=""
  local db_mount=""
  local key_mount=""
  local db_covered="0"
  local key_covered="0"
  local status=0

  docker_db_path="/data/usage.sqlite"
  docker_data_key_path="/data/data.key"
  docker_data_volume_root=""
  docker_compose_config_loaded="0"
  docker_compose_config_source=""
  [ -f "$install_dir/compose.yaml" ] || return 0
  if load_docker_compose_config; then
    :
  else
    status=$?
    die "Unable to read the existing Docker Compose configuration; refusing to resolve Manager data paths (exit status $status)."
  fi

  # Only a variable declared for the cpa-manager-plus service is part of the
  # container's effective environment. A same-named value that exists only in
  # the project .env is merely an interpolation candidate; treating it as an
  # implicit container variable could make an upgrade operate on a database
  # the running Manager Server never opened.
  if value="$(compose_service_environment_value USAGE_DB_PATH)"; then
    [ -n "$value" ] && docker_db_path="$value"
  else
    status=$?
    [ "$status" -eq 1 ] || die "Unable to resolve USAGE_DB_PATH from the existing Docker Compose configuration."
  fi
  if value="$(compose_service_environment_value CPA_MANAGER_DATA_KEY_PATH)"; then
    [ -n "$value" ] && docker_data_key_path="$value"
  else
    status=$?
    [ "$status" -eq 1 ] || die "Unable to resolve CPA_MANAGER_DATA_KEY_PATH from the existing Docker Compose configuration."
  fi
  validate_docker_container_path "USAGE_DB_PATH" "$docker_db_path"
  validate_docker_container_path "CPA_MANAGER_DATA_KEY_PATH" "$docker_data_key_path"

  mount_targets="$(docker_compose_volume_targets || true)"
  while IFS= read -r target; do
    [ -n "$target" ] || continue
    if docker_path_is_under_mount "$docker_db_path" "$target"; then
      db_covered="1"
      if [ -z "$db_mount" ] && [ "$target" != "$docker_db_path" ] && [ "$target" != "$docker_data_key_path" ]; then
        db_mount="$target"
      fi
    fi
    if docker_path_is_under_mount "$docker_data_key_path" "$target"; then
      key_covered="1"
      if [ -z "$key_mount" ] && [ "$target" != "$docker_db_path" ] && [ "$target" != "$docker_data_key_path" ]; then
        key_mount="$target"
      fi
    fi
  done <<< "$mount_targets"

  if [ "$docker_db_path" != "/data/usage.sqlite" ] || [ "$docker_data_key_path" != "/data/data.key" ]; then
    [ "$db_covered" = "1" ] || die "USAGE_DB_PATH ($docker_db_path) is not covered by a cpa-manager-plus volume mount; refusing to operate on an unmounted database."
    [ "$key_covered" = "1" ] || die "CPA_MANAGER_DATA_KEY_PATH ($docker_data_key_path) is not covered by a cpa-manager-plus volume mount; refusing to operate on an unmounted data key."
    [ -n "$db_mount" ] || [ -n "$key_mount" ] ||
      die "Custom Docker database and data-key paths are covered only by exact file mounts; a directory volume mount is required for the migration snapshot."
  fi
  if [ -n "$db_mount" ]; then
    docker_data_volume_root="$db_mount"
  elif [ -n "$key_mount" ]; then
    docker_data_volume_root="$key_mount"
  else
    docker_data_volume_root="/data"
  fi
}

locate_existing_native_config() {
  local run_binary_dir=""
  local runtime_package=""
  local config=""
  local found=""

  runtime_package="$(awk '
    /^# CPAMP_RUNTIME_PACKAGE=/ {
      sub(/^# CPAMP_RUNTIME_PACKAGE=/, "")
      print
      exit
    }
  ' "$install_dir/run.sh")"
  if [ -n "$runtime_package" ]; then
    validate_native_runtime_package "$runtime_package"
    config="$install_dir/runtime/$runtime_package/config.json"
    [ -f "$config" ] || die "Native runtime marker points to a missing config: $config"
    native_existing_binary_dir="$(cd "$(dirname "$config")" && pwd -P)"
    native_existing_config_file="$native_existing_binary_dir/config.json"
    return
  fi

  run_binary_dir="$(awk '
    /^[[:space:]]*cd[[:space:]]+"[^"]+"[[:space:]]*$/ {
      value = $0
      sub(/^[[:space:]]*cd[[:space:]]+"/, "", value)
      sub(/"[[:space:]]*$/, "", value)
      print value
      exit
    }
  ' "$install_dir/run.sh")"
  if [ -n "$run_binary_dir" ]; then
    case "$run_binary_dir" in
      /*) ;;
      *) run_binary_dir="$install_dir/$run_binary_dir" ;;
    esac
    if [ -f "$run_binary_dir/config.json" ]; then
      native_existing_binary_dir="$(cd "$run_binary_dir" && pwd -P)"
      native_existing_config_file="$native_existing_binary_dir/config.json"
      return
    fi
  fi

  for config in "$install_dir"/runtime/*/config.json; do
    [ -f "$config" ] || continue
    if [ -n "$found" ]; then
      die "Unable to determine the active native runtime from $install_dir/run.sh; multiple runtime configs exist."
    fi
    found="$config"
  done
  [ -n "$found" ] || die "No native runtime config was found under $install_dir/runtime."
  native_existing_binary_dir="$(cd "$(dirname "$found")" && pwd -P)"
  native_existing_config_file="$native_existing_binary_dir/config.json"
}

load_existing_native_config() {
  local config_dir=""
  local http_addr=""
  local raw_data_dir=""
  local raw_db_path=""
  local raw_data_key_path=""
  local raw_admin_key_file=""
  local raw_cpa_key_file=""
  local has_cpa_url="0"
  local has_cpa_key_file="0"
  local value=""

  deploy_method="native"
  install_mode="cpamp"
  cpamp_version="${CPAMP_VERSION:-latest}"
  validate_version_value "$(text version)" "$cpamp_version"
  locate_existing_native_config
  config_dir="$(dirname "$native_existing_config_file")"
  native_existing_config_dir="$config_dir"

  if json_key_declared "$native_existing_config_file" httpAddr; then
    http_addr="$(require_json_string_value "$native_existing_config_file" httpAddr)"
  else
    http_addr="0.0.0.0:18317"
  fi
  cpamp_port="${CPAMP_PORT:-${http_addr##*:}}"
  normalize_port "$cpamp_port" || die "Invalid CPAMP port in existing native config: $cpamp_port"

  if json_key_declared "$native_existing_config_file" dataDir; then
    raw_data_dir="$(require_json_string_value "$native_existing_config_file" dataDir)"
  else
    raw_data_dir="./data"
  fi
  native_data_dir="$(resolve_native_config_path "$raw_data_dir" "$config_dir")"
  if json_key_declared "$native_existing_config_file" dbPath; then
    native_config_db_declared="1"
    raw_db_path="$(require_json_string_value "$native_existing_config_file" dbPath)"
    native_db_path="$(resolve_native_config_path "$raw_db_path" "$config_dir")"
  else
    native_config_db_declared="0"
    native_db_path="$native_data_dir/usage.sqlite"
  fi
  if json_key_declared "$native_existing_config_file" dataKeyPath; then
    native_config_data_key_declared="1"
    raw_data_key_path="$(require_json_string_value "$native_existing_config_file" dataKeyPath)"
    native_data_key_path="$(resolve_native_config_path "$raw_data_key_path" "$config_dir")"
  else
    native_config_data_key_declared="0"
    native_data_key_path="$native_data_dir/data.key"
  fi
  native_config_data_dir="$native_data_dir"
  native_config_db_path="$native_db_path"
  native_config_data_key_path="$native_data_key_path"
  resolve_native_runtime_paths
  require_native_manager_data_files

  raw_admin_key_file="$(require_json_string_value "$native_existing_config_file" adminKeyFile)"
  native_admin_key_file="$(resolve_native_config_path "$raw_admin_key_file" "$config_dir")"
  admin_key="$(read_existing_secret "$native_admin_key_file")" ||
    die "Admin key file is missing: $native_admin_key_file"

  if json_key_declared "$native_existing_config_file" cpaUpstreamUrl; then
    has_cpa_url="1"
    cpa_url="$(require_json_string_value "$native_existing_config_file" cpaUpstreamUrl)"
  fi
  if json_key_declared "$native_existing_config_file" managementKeyFile; then
    has_cpa_key_file="1"
    raw_cpa_key_file="$(require_json_string_value "$native_existing_config_file" managementKeyFile)"
  fi
  if [ "$has_cpa_url" = "1" ] || [ "$has_cpa_key_file" = "1" ]; then
    [ "$has_cpa_url" = "1" ] && [ "$has_cpa_key_file" = "1" ] ||
      die "Existing native config must contain both cpaUpstreamUrl and managementKeyFile for migration."
    validate_url_value "$(text cpa_url)" "$cpa_url"
    cpa_management_key_file="$(resolve_native_config_path "$raw_cpa_key_file" "$config_dir")"
    if is_installer_owned_cpa_key_file "$cpa_management_key_file"; then
      cpa_management_key_cleanup_allowed="1"
      cpa_import_retry_state_required="1"
      cpa_management_key="$(read_existing_secret "$cpa_management_key_file" "1")" ||
        die "CPA Management Key file is missing: $cpa_management_key_file"
    else
      cpa_management_key="$(read_existing_secret "$cpa_management_key_file" "0")" ||
        die "CPA Management Key file is missing: $cpa_management_key_file"
    fi
    cpa_connection_mode="env"
    if [ -e "$(cpa_import_retry_path)" ] || [ -L "$(cpa_import_retry_path)" ]; then
      load_and_validate_cpa_import_retry_state
    fi
  else
    if load_cpa_import_retry_state; then
      :
    elif load_explicit_cpa_import_recovery; then
      :
    else
      cpa_connection_mode="stored"
    fi
  fi

  for value in "$native_data_dir" "$native_db_path" "$native_data_key_path" "$native_admin_key_file"; do
    validate_single_line "Native config path" "$value"
  done
  detect_installer_managed_cpa_key_pending_cleanup
}

load_existing_docker_config() {
  local value=""
  local legacy_key_file=""
  local legacy_key_dir=""
  local legacy_key_file_external="0"
  local compose_has_cpa_url="0"
  local compose_has_cpa_key="0"
  local compose_has_cpa_key_file="0"
  local compose_has_managed_cpa_secret="0"
  local effective_key=""
  local effective_key_file=""
  local status="0"

  [ -f "$install_dir/.env" ] || die "Missing existing config: $install_dir/.env"
  [ -f "$install_dir/compose.yaml" ] || die "Missing existing config: $install_dir/compose.yaml"
  deploy_method="docker"
  resolve_docker_container_paths
  cpamp_image="$(read_env_value "$install_dir/.env" CPAMP_IMAGE 2>/dev/null || printf '%s' "$default_cpamp_image")"
  validate_image_ref "$(text cpamp_image)" "$cpamp_image"
  cpamp_port="$(read_env_value "$install_dir/.env" CPAMP_PORT 2>/dev/null || printf '18317')"
  normalize_port "$cpamp_port" || die "Invalid CPAMP port in existing .env: $cpamp_port"
  compose_service_references_token CPA_UPSTREAM_URL && compose_has_cpa_url="1"
  compose_service_references_token CPA_MANAGEMENT_KEY && compose_has_cpa_key="1"
  compose_service_references_token CPA_MANAGEMENT_KEY_FILE && compose_has_cpa_key_file="1"
  compose_service_references_token cpa_management_key && compose_has_managed_cpa_secret="1"
  if grep -q '^[[:space:]]*cli-proxy-api:' "$install_dir/compose.yaml"; then
    install_mode="stack"
    cpa_image="$(read_env_value "$install_dir/.env" CPA_IMAGE 2>/dev/null || printf '%s' "$default_cpa_image")"
    validate_image_ref "$(text cpa_image)" "$cpa_image"
    cpa_port="$(read_env_value "$install_dir/.env" CPA_PORT 2>/dev/null || printf '8317')"
    normalize_port "$cpa_port" || die "Invalid CPA port in existing .env: $cpa_port"
    cpa_url="http://cli-proxy-api:8317"
    if [ "$compose_has_cpa_key" = "1" ] ||
       [ "$compose_has_cpa_key_file" = "1" ] ||
       [ "$compose_has_managed_cpa_secret" = "1" ]; then
      cpa_connection_mode="env"
    else
      cpa_connection_mode="stored"
    fi
  else
    install_mode="cpamp"
    if [ "$compose_has_cpa_key" = "1" ] ||
       [ "$compose_has_cpa_key_file" = "1" ] ||
       [ "$compose_has_managed_cpa_secret" = "1" ]; then
      cpa_connection_mode="env"
      if [ "$compose_has_cpa_url" = "1" ]; then
        if [ "${CPA_UPSTREAM_URL+x}" = "x" ]; then
          cpa_url="${CPA_UPSTREAM_URL-}"
        else
          cpa_url="$(read_env_value "$install_dir/.env" CPA_UPSTREAM_URL 2>/dev/null || true)"
        fi
      fi
    else
      cpa_connection_mode="setup"
    fi
  fi
  if [ "$cpa_connection_mode" = "env" ]; then
    if [ "$compose_has_cpa_key" = "1" ]; then
      # Read the resolved service value first so a literal Compose environment
      # entry (not just a .env interpolation) is also migrated. The resolved
      # value already reflects Compose's process-environment precedence for
      # interpolated entries.
      if effective_key="$(compose_service_environment_value CPA_MANAGEMENT_KEY)"; then
        :
      else
        status=$?
        # Status 1 means the small raw-file parser did not find a usable value;
        # keep the existing .env/process-environment fallback for that case.
        # Any other status is a parse or interpolation error and must remain
        # fail-closed instead of being widened into a legacy-key fallback.
        [ "$status" -eq 1 ] ||
          die "Unable to resolve CPA_MANAGEMENT_KEY from the existing Docker Compose configuration."
        if [ "${CPA_MANAGEMENT_KEY+x}" = "x" ]; then
          effective_key="${CPA_MANAGEMENT_KEY-}"
        else
          effective_key="$(read_env_value "$install_dir/.env" CPA_MANAGEMENT_KEY 2>/dev/null || true)"
        fi
      fi
    fi
    if [ -n "$effective_key" ]; then
      cpa_management_key="$effective_key"
      cpa_management_key_cleanup_allowed="1"
      cpa_management_key_inline_input="1"
      # A symlink at the managed path (valid or dangling) is externally
      # managed: materialize the inline key into an installer-owned import
      # copy next to it instead of replacing or writing through the symlink.
      if [ -e "$install_dir/secrets/cpa-management-key" ] || [ -L "$install_dir/secrets/cpa-management-key" ]; then
        cpa_management_key_file="$install_dir/secrets/cpa-management-key.import.$$"
        cpa_management_key_import_copy="1"
      else
        cpa_management_key_file="$install_dir/secrets/cpa-management-key"
      fi
      cpa_import_retry_state_required="1"
    else
      if [ "$compose_has_cpa_key_file" = "1" ]; then
        if [ "${CPA_MANAGEMENT_KEY_FILE+x}" = "x" ]; then
          effective_key_file="${CPA_MANAGEMENT_KEY_FILE-}"
        else
          effective_key_file="$(read_env_value "$install_dir/.env" CPA_MANAGEMENT_KEY_FILE 2>/dev/null || true)"
        fi
      fi
      if [ -n "$effective_key_file" ]; then
        legacy_key_file="$effective_key_file"
        legacy_key_file_external="1"
      elif [ "$compose_has_managed_cpa_secret" = "1" ]; then
        legacy_key_file="$install_dir/secrets/cpa-management-key"
      fi
    fi
    if [ -z "$cpa_management_key" ] && [ -n "$legacy_key_file" ]; then
      case "$legacy_key_file" in
        /*) ;;
        *) legacy_key_file="$install_dir/$legacy_key_file" ;;
      esac
      legacy_key_dir="$(cd "$(dirname "$legacy_key_file")" 2>/dev/null && pwd -P)" ||
        die "CPA Management Key file parent does not exist: $legacy_key_file"
      legacy_key_file="$legacy_key_dir/$(basename "$legacy_key_file")"
      cpa_management_key_file="$legacy_key_file"
      cpa_management_key_cleanup_allowed="0"
      if [ "$legacy_key_file_external" = "1" ] && path_is_managed_cpa_key_file "$legacy_key_file"; then
        mark_cpa_management_key_external ||
          die "Unable to record external CPA Management Key ownership: $legacy_key_file"
      fi
      if [ "$legacy_key_file_external" != "1" ] && is_installer_owned_cpa_key_file "$legacy_key_file"; then
        cpa_management_key_cleanup_allowed="1"
        cpa_management_key="$(read_existing_secret "$legacy_key_file" "1")" ||
          die "CPA Management Key is referenced by the existing Docker configuration but the key file is not readable: $legacy_key_file"
        cpa_import_retry_state_required="1"
      else
        cpa_management_key="$(read_existing_secret "$legacy_key_file" "0")" ||
          die "CPA Management Key is referenced by the existing Docker configuration but the key file is not readable: $legacy_key_file"
      fi
    fi
    if [ -z "$cpa_management_key" ]; then
      die "CPA Management Key is referenced by the existing Docker configuration but no readable key input was found."
    fi
    validate_secret_value "CPA Management Key" "$cpa_management_key"
    validate_url_value "$(text cpa_url)" "$cpa_url"
    if [ -e "$(cpa_import_retry_path)" ] || [ -L "$(cpa_import_retry_path)" ]; then
      load_and_validate_cpa_import_retry_state
    fi
  else
    if load_cpa_import_retry_state; then
      :
    elif load_explicit_cpa_import_recovery; then
      :
    fi
  fi
  if value="$(read_existing_secret "$install_dir/secrets/cpamp-admin-key")"; then
    admin_key="$value"
  elif [ "$operation" = "repair" ]; then
    admin_secret_missing="1"
  else
    die "Admin key file is missing: $install_dir/secrets/cpamp-admin-key. Run with CPAMP_OPERATION=repair."
  fi
  detect_installer_managed_cpa_key_pending_cleanup
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

validate_version_value() {
  local label="$1"
  local value="$2"
  validate_single_line "$label" "$value"
  case "$value" in
    *[!A-Za-z0-9._+-]*) die "$label contains unsupported characters." ;;
  esac
}

validate_native_runtime_package() {
  local value="$1"
  validate_single_line "Native runtime package" "$value"
  case "$value" in
    cpa-manager-plus_*) ;;
    *) die "Native runtime package is not recognized: $value" ;;
  esac
  case "$value" in
    *[!A-Za-z0-9._+-]*) die "Native runtime package contains unsupported characters." ;;
  esac
}

yaml_double_quote_escape() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  printf '%s\n' "$value"
}

systemd_double_quote_escape() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  value="${value//%/%%}"
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
    validate_version_value "$(text version)" "$cpamp_version"
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
      if [ "$cpa_connection_mode" = "env" ]; then
        say "$(text cpa_connection_label): $(text cpa_connection_env)"
      else
        say "$(text cpa_connection_label): $(text cpa_imported)"
      fi
    else
      if [ "$cpa_connection_mode" = "env" ]; then
        say "$(text cpa_connection_label): $(text cpa_connection_env)"
      elif [ "$cpa_connection_mode" = "stored" ]; then
        say "$(text cpa_connection_label): $(text cpa_imported)"
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
    elif [ "$cpa_connection_mode" = "stored" ]; then
      say "$(text cpa_connection_label): $(text cpa_imported)"
    else
      say "$(text cpa_connection_label): $(text cpa_connection_setup)"
    fi
    if [ "$cpa_connection_mode" = "env" ]; then
      say "$(text cpa_url): $cpa_url"
    fi
  fi
  if [ "$installer_managed_cpa_key_pending_cleanup" = "1" ]; then
    say "$(text cpa_key_pending_cleanup)"
  fi
  if [ "$cpa_import_retry_state_loaded" = "1" ]; then
    say "$(text cpa_import_retry_loaded)"
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
    if [ "$operation" = "upgrade" ] && [ "$existing_install_state" = "native-managed" ] &&
       [ "$dry_run" != "1" ] && [ "$skip_execute" != "1" ]; then
      # Native rollback must be able to bind the spawned PID to its recorded
      # process start time before it mutates or restores the data file-set.
      require_command ps
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
  [ "${CPAMP_OVERWRITE:-0}" = "1" ] || [ "$operation" = "regenerate" ] || [ "$operation" = "upgrade" ]
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
    if [ ! -L "$file" ]; then
      chmod 600 "$file" 2>/dev/null || die "Unable to restrict secret file permissions: $file"
    fi
    existing="$(< "$file")"
    existing="${existing%$'\r'}"
    validate_secret_value "$file" "$existing"
    printf '%s\n' "$existing"
    return
  fi
  if [ -L "$file" ]; then
    die "Refusing to write through a symlinked secret path: $file"
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
    cpa_management_key_file="$install_dir/secrets/cpa-management-key"
    if is_installer_owned_cpa_key_file "$cpa_management_key_file"; then
      cpa_management_key_cleanup_allowed="1"
    fi
    cpa_import_retry_state_required="1"
    generated_demo_client_key="sk-$(random_alnum 64)"
    demo_client_key="$(ensure_secret_file "$install_dir/secrets/cpa-demo-client-key" "$generated_demo_client_key")"
    write_cpa_config
  elif [ "$cpa_connection_mode" = "env" ]; then
    cpa_management_key="$(ensure_secret_file "$install_dir/secrets/cpa-management-key" "$cpa_management_key")"
    cpa_management_key_file="$install_dir/secrets/cpa-management-key"
    if is_installer_owned_cpa_key_file "$cpa_management_key_file"; then
      cpa_management_key_cleanup_allowed="1"
    fi
    cpa_import_retry_state_required="1"
  fi

  write_docker_compose
}

needs_cpa_connection_import() {
  [ "$cpa_connection_mode" = "env" ] && [ -n "$cpa_url" ] && [ -n "$cpa_management_key" ]
}

print_docker_connection_import_command() {
  local key_file="${cpa_management_key_file:-$install_dir/secrets/cpa-management-key}"
  say "cd \"$install_dir\" && docker compose run --rm --no-deps -e CPA_UPSTREAM_URL= -e CPA_MANAGEMENT_KEY= -e CPA_MANAGEMENT_KEY_FILE=/dev/null -v \"$key_file:/run/cpamp-import/cpa-management-key:ro\" cpa-manager-plus store-cpa-connection --cpa-base-url \"$cpa_url\" --management-key-file /run/cpamp-import/cpa-management-key --db-path \"$docker_db_path\" --data-key-path \"$docker_data_key_path\""
}

print_full_upgrade_command() {
  local quoted_lang=""
  local quoted_install_dir=""
  local quoted_script=""
  local quoted_version=""
  printf -v quoted_lang '%q' "$lang_code"
  printf -v quoted_install_dir '%q' "$install_dir"
  printf -v quoted_script '%q' "$installer_script_path"
  if [ "$existing_install_state" = "native-managed" ]; then
    printf -v quoted_version '%q' "$cpamp_version"
    say "CPAMP_OPERATION=upgrade CPAMP_SKIP_EXECUTE=0 CPAMP_NON_INTERACTIVE=1 CPAMP_CONFIRM=1 CPAMP_LANG=$quoted_lang CPAMP_VERSION=$quoted_version CPAMP_INSTALL_DIR=$quoted_install_dir bash $quoted_script"
  else
    say "CPAMP_OPERATION=upgrade CPAMP_SKIP_EXECUTE=0 CPAMP_NON_INTERACTIVE=1 CPAMP_CONFIRM=1 CPAMP_LANG=$quoted_lang CPAMP_INSTALL_DIR=$quoted_install_dir bash $quoted_script"
  fi
}

print_docker_post_import_validation_commands() {
  local key_file="${cpa_management_key_file:-$install_dir/secrets/cpa-management-key}"
  say "cd \"$install_dir\" && docker compose exec -T cpa-manager-plus wget -qO- http://127.0.0.1:18317/health"
  say "cd \"$install_dir\" && CPAMP_ADMIN_KEY=\"\$(< \"$install_dir/secrets/cpamp-admin-key\")\" && docker compose exec -T cpa-manager-plus wget -qO- --header=\"Authorization: Bearer \$CPAMP_ADMIN_KEY\" http://127.0.0.1:18317/status"
  say "cd \"$install_dir\" && CPAMP_ADMIN_KEY=\"\$(< \"$install_dir/secrets/cpamp-admin-key\")\" && docker compose exec -T cpa-manager-plus wget -qO- --post-data='' --header=\"Authorization: Bearer \$CPAMP_ADMIN_KEY\" http://127.0.0.1:18317/v0/management/cpa-connection/validate"
  if [ "$cpa_management_key_cleanup_allowed" = "1" ] && [ -n "$key_file" ]; then
    say "rm -f \"$key_file\""
  fi
}

run_docker_connection_import() {
  local key_file="${cpa_management_key_file:-$install_dir/secrets/cpa-management-key}"
  materialize_cpa_management_key_file
  persist_cpa_import_retry_state
  (
    cd "$install_dir"
    docker compose run --rm --no-deps \
      -e CPA_UPSTREAM_URL= \
      -e CPA_MANAGEMENT_KEY= \
      -e CPA_MANAGEMENT_KEY_FILE=/dev/null \
      -v "$key_file:/run/cpamp-import/cpa-management-key:ro" \
      cpa-manager-plus store-cpa-connection \
      --cpa-base-url "$cpa_url" \
      --management-key-file /run/cpamp-import/cpa-management-key \
      --db-path "$docker_db_path" \
      --data-key-path "$docker_data_key_path"
  )
}

run_docker_data_snapshot() {
  local action="$1"
  (
    cd "$install_dir"
    if [ "$action" = "delete" ]; then
      docker compose run --rm --no-deps \
        -e CPA_UPSTREAM_URL= \
        -e CPA_MANAGEMENT_KEY= \
        -e CPA_MANAGEMENT_KEY_FILE=/dev/null \
        cpa-manager-plus manager-data-snapshot delete \
        --snapshot-dir "$docker_data_snapshot_dir"
    else
      docker compose run --rm --no-deps \
        -e CPA_UPSTREAM_URL= \
        -e CPA_MANAGEMENT_KEY= \
        -e CPA_MANAGEMENT_KEY_FILE=/dev/null \
        cpa-manager-plus manager-data-snapshot "$action" \
        --snapshot-dir "$docker_data_snapshot_dir" \
        --db-path "$docker_db_path" \
        --data-key-path "$docker_data_key_path"
    fi
  )
}

prepare_docker_data_snapshot() {
  local timestamp=""
  timestamp="$(date '+%Y%m%d%H%M%S')"
  docker_data_snapshot_dir="${docker_data_volume_root:-/data}/.cpamp-manager-snapshot-$timestamp-$$"
  run_docker_data_snapshot create || return 1
  docker_data_snapshot_created="1"
}

verify_docker_container_paths() {
  if [ "$operation" != "upgrade" ] || [ "$existing_install_state" != "managed" ]; then
    return 0
  fi
  if [ "$dry_run" = "1" ] || [ "$skip_execute" = "1" ]; then
    return 0
  fi
  (
    cd "$install_dir"
    docker compose run --rm --no-deps --entrypoint /bin/sh cpa-manager-plus \
      -c 'test -f "$1" && test -r "$1"' sh "$docker_db_path" &&
      docker compose run --rm --no-deps --entrypoint /bin/sh cpa-manager-plus \
        -c 'test -f "$1" && test -r "$1"' sh "$docker_data_key_path"
  )
}

delete_docker_data_snapshot() {
  [ "$docker_data_snapshot_created" = "1" ] || return 0
  run_docker_data_snapshot delete || return 1
  docker_data_snapshot_created="0"
}

cleanup_committed_legacy_cpa_runtime_backups() {
  local backup=""
  local failed="0"
  for backup in "$legacy_compose_backup" "$legacy_env_backup"; do
    [ -n "$backup" ] || continue
    if ! rm -f "$backup"; then
      failed="1"
      printf '%s\n' "$(text cpa_runtime_backup_cleanup_failed) File: $backup" >&2
    fi
  done
  return "$failed"
}

backup_legacy_cpa_runtime_config() {
  local timestamp=""
  timestamp="$(date '+%Y%m%d%H%M%S')"
  legacy_compose_backup="$install_dir/compose.yaml.cpa-key-migration.bak.$timestamp.$$"
  legacy_env_backup="$install_dir/.env.cpa-key-migration.bak.$timestamp.$$"
  cp -p "$install_dir/compose.yaml" "$legacy_compose_backup" || return 1
  cp -p "$install_dir/.env" "$legacy_env_backup" || return 1
}

remove_legacy_cpa_runtime_config() {
  local compose_tmp="$install_dir/compose.yaml.cpa-key-migration.tmp.$$"
  local env_tmp="$install_dir/.env.cpa-key-migration.tmp.$$"

  if ! awk '
    function indentation(value) {
      match(value, /^[[:space:]]*/)
      return RLENGTH
    }
    BEGIN {
      services_indent = -1
      cpa_indent = -1
      environment_indent = -1
      secrets_indent = -1
      in_services = 0
      in_cpa_service = 0
      in_environment = 0
      in_secrets = 0
      in_top_level_secrets = 0
      top_level_secrets_indent = -1
      skip_top_level_secret = 0
      top_level_secret_indent = -1
      skip_long_secret_item = 0
      skip_long_secret_indent = -1
    }
    {
      line = $0
      current_indent = indentation(line)

      if (skip_long_secret_item) {
        if (line ~ /^[[:space:]]*$/ || current_indent > skip_long_secret_indent) {
          next
        }
        skip_long_secret_item = 0
        skip_long_secret_indent = -1
      }

      if (in_cpa_service && line !~ /^[[:space:]]*$/ && current_indent <= cpa_indent) {
        in_cpa_service = 0
        in_environment = 0
        in_secrets = 0
        environment_indent = -1
        secrets_indent = -1
      }

      if (in_environment) {
        if (line ~ /^[[:space:]]*$/) {
          print
          next
        }
        if (current_indent <= environment_indent) {
          in_environment = 0
          environment_indent = -1
        } else if (line ~ /^[[:space:]]*["\047]?CPA_UPSTREAM_URL["\047]?[[:space:]]*:/ || line ~ /^[[:space:]]*["\047]?CPA_MANAGEMENT_KEY_FILE["\047]?[[:space:]]*:/ || line ~ /^[[:space:]]*["\047]?CPA_MANAGEMENT_KEY["\047]?[[:space:]]*:/ || line ~ /^[[:space:]]*-[[:space:]]*["\047]?CPA_UPSTREAM_URL["\047]?[=:]/ || line ~ /^[[:space:]]*-[[:space:]]*["\047]?CPA_MANAGEMENT_KEY_FILE["\047]?[=:]/ || line ~ /^[[:space:]]*-[[:space:]]*["\047]?CPA_MANAGEMENT_KEY["\047]?[=:]/) {
          next
        }
        if (in_environment) {
          print
          next
        }
      }

      if (in_secrets) {
        if (line ~ /^[[:space:]]*$/) {
          print
          next
        }
        if (current_indent <= secrets_indent) {
          in_secrets = 0
          secrets_indent = -1
        } else if (line ~ /^[[:space:]]*-[[:space:]]*["\047]?cpa_management_key["\047]?[[:space:]]*(#.*)?$/) {
          next
        } else if (line ~ /^[[:space:]]*-[[:space:]]*source[[:space:]]*:[[:space:]]*["\047]?cpa_management_key["\047]?[[:space:]]*(#.*)?$/) {
          skip_long_secret_item = 1
          skip_long_secret_indent = current_indent
          next
        }
        if (in_secrets) {
          print
          next
        }
      }

      if (in_top_level_secrets) {
        if (skip_top_level_secret) {
          if (line ~ /^[[:space:]]*$/ || current_indent > top_level_secret_indent) {
            next
          }
          skip_top_level_secret = 0
          top_level_secret_indent = -1
        }
        if (line !~ /^[[:space:]]*$/ && current_indent <= top_level_secrets_indent) {
          in_top_level_secrets = 0
          top_level_secrets_indent = -1
        } else if (line ~ /^[[:space:]]*$/) {
          print
          next
        } else if (current_indent > top_level_secrets_indent && line ~ /^[[:space:]]*["\047]?cpa_management_key["\047]?[[:space:]]*:/) {
          skip_top_level_secret = 1
          top_level_secret_indent = current_indent
          next
        } else if (in_top_level_secrets) {
          print
          next
        }
      }

      if (line ~ /^[[:space:]]*services:[[:space:]]*$/) {
        services_indent = current_indent
        in_services = 1
      } else if (in_services && line !~ /^[[:space:]]*$/ && current_indent <= services_indent) {
        in_services = 0
      }

      if (!in_cpa_service && in_services &&
          line ~ /^[[:space:]]*["\047]?cpa-manager-plus["\047]?:[[:space:]]*$/) {
        cpa_indent = current_indent
        in_cpa_service = 1
      }

      if (in_cpa_service && line ~ /^[[:space:]]*environment:[[:space:]]*$/) {
        in_environment = 1
        environment_indent = current_indent
        in_secrets = 0
        secrets_indent = -1
        print
        next
      }

      if (in_cpa_service && line ~ /^[[:space:]]*secrets:[[:space:]]*$/) {
        in_secrets = 1
        secrets_indent = current_indent
        in_environment = 0
        environment_indent = -1
        print
        next
      }

      if (line ~ /^[[:space:]]*secrets:[[:space:]]*$/ && current_indent == 0) {
        in_top_level_secrets = 1
        top_level_secrets_indent = current_indent
        print
        next
      }

      print
    }
  ' "$install_dir/compose.yaml" > "$compose_tmp"; then
    rm -f "$compose_tmp" "$env_tmp"
    return 1
  fi
  if grep -Eq 'CPA_UPSTREAM_URL|CPA_MANAGEMENT_KEY_FILE|CPA_MANAGEMENT_KEY|cpa_management_key' "$compose_tmp"; then
    rm -f "$compose_tmp" "$env_tmp"
    return 1
  fi
  if ! awk '!/^[[:space:]]*(export[[:space:]]+)?CPA_UPSTREAM_URL[[:space:]]*=/ && !/^[[:space:]]*(export[[:space:]]+)?CPA_MANAGEMENT_KEY[[:space:]]*=/ && !/^[[:space:]]*(export[[:space:]]+)?CPA_MANAGEMENT_KEY_FILE[[:space:]]*=/' "$install_dir/.env" > "$env_tmp"; then
    rm -f "$compose_tmp" "$env_tmp"
    return 1
  fi
  if ! mv -f "$compose_tmp" "$install_dir/compose.yaml"; then
    rm -f "$compose_tmp" "$env_tmp"
    return 1
  fi
  if ! mv -f "$env_tmp" "$install_dir/.env"; then
    rm -f "$env_tmp"
    return 1
  fi
}

restore_legacy_cpa_runtime_config() {
  local failed="0"
  if [ -n "$legacy_compose_backup" ]; then
    if [ -f "$legacy_compose_backup" ]; then
      cp -p "$legacy_compose_backup" "$install_dir/compose.yaml" || failed="1"
    else
      failed="1"
    fi
  fi
  if [ -n "$legacy_env_backup" ]; then
    if [ -f "$legacy_env_backup" ]; then
      cp -p "$legacy_env_backup" "$install_dir/.env" || failed="1"
    else
      failed="1"
    fi
  fi
  [ "$failed" = "0" ]
}

rollback_legacy_cpa_runtime_config() {
  local failed="0"
  local process_safe="1"
  cpa_connection_rollback_pending="0"
  if ! (
    cd "$install_dir"
    docker compose stop cpa-manager-plus
  ); then
    failed="1"
    process_safe="0"
  fi
  if [ "$process_safe" = "1" ] && [ "$docker_data_snapshot_created" = "1" ]; then
    run_docker_data_snapshot restore || failed="1"
  fi
  if [ "$process_safe" = "1" ] && [ "$failed" = "0" ] && [ "$legacy_cpa_runtime_config_modified" = "1" ]; then
    restore_legacy_cpa_runtime_config || failed="1"
  fi
  if [ "$process_safe" = "1" ] && [ "$failed" = "0" ] && ! (
    cd "$install_dir"
    docker compose up -d
  ); then
    failed="1"
  fi
  if [ "$failed" = "0" ]; then
    # The business rollback already succeeded; a snapshot cleanup failure only
    # leaves the rollback artifact behind and must not report the rollback
    # itself as failed.
    if ! delete_docker_data_snapshot; then
      printf '%s\n' "$(text cpa_snapshot_cleanup_failed) Snapshot: $docker_data_snapshot_dir" >&2
    fi
  fi
  if [ "$failed" = "1" ]; then
    printf '%s\n' "$(text cpa_rollback_failed) Snapshot: ${docker_data_snapshot_dir:-not-created}" >&2
    return 1
  fi
}

commit_legacy_cpa_runtime_config() {
  # Business commit boundary: health, admin auth, CPA proxy validation, and
  # the runtime config migration all succeeded. The rollback flag must be
  # cleared before snapshot cleanup so a cleanup failure can never roll back
  # an already-verified deployment; a failed cleanup only keeps the snapshot.
  cpa_connection_rollback_pending="0"
  if ! delete_docker_data_snapshot; then
    printf '%s\n' "$(text cpa_snapshot_cleanup_failed) Snapshot: $docker_data_snapshot_dir" >&2
  fi
  if ! cleanup_committed_legacy_cpa_runtime_backups; then
    :
  fi
}

finalize_cpa_connection_import() {
  if [ "$dry_run" = "1" ] || [ "$skip_execute" = "1" ]; then
    return
  fi
  if [ "$cpa_connection_imported" = "1" ]; then
    if [ "$cpa_import_retry_state_required" = "1" ] && [ -n "$cpa_import_retry_state_file" ]; then
      if ! rm -f "$cpa_import_retry_state_file"; then
        printf '%s\n' "$(text cpa_import_state_cleanup_failed) File: $cpa_import_retry_state_file" >&2
        return
      fi
      cpa_import_retry_state_required="0"
      cpa_import_retry_state_loaded="0"
      cpa_import_retry_state_file=""
    fi
    # Only genuinely installer-owned key files may be removed: the managed
    # path re-checked through is_installer_owned_cpa_key_file, or an import
    # copy this run created next to an externally managed symlink/file.
    if [ "$cpa_management_key_cleanup_allowed" = "1" ] && [ -n "$cpa_management_key_file" ]; then
      if [ "$cpa_management_key_import_copy" = "1" ] || is_installer_owned_cpa_key_file "$cpa_management_key_file"; then
        if ! rm -f "$cpa_management_key_file"; then
          printf '%s\n' "$(text cpa_key_cleanup_failed) File: $cpa_management_key_file" >&2
        else
          installer_managed_cpa_key_pending_cleanup="0"
        fi
      else
        printf '%s\n' "Skipping CPA key cleanup: file is not installer-owned: $cpa_management_key_file" >&2
      fi
    fi
  elif [ "$installer_managed_cpa_key_pending_cleanup" = "1" ] && [ "$cpa_proxy_validation_passed" = "1" ]; then
    # Cleanup boundary for a pending finalization from an earlier run: the
    # stored SQLite connection was re-verified through the CPA proxy during this
    # run, so the leftover installer-managed plaintext key can be removed. A
    # verification failure already aborted the run and kept the file for retry.
    if ! rm -f "$installer_managed_cpa_key_pending_file"; then
      printf '%s\n' "$(text cpa_key_cleanup_failed) File: $installer_managed_cpa_key_pending_file" >&2
    else
      installer_managed_cpa_key_pending_cleanup="0"
    fi
  fi
  # Import copies from earlier failed runs hold the same plaintext key and are
  # never an input for this run's migration; remove them only after the stored
  # connection has been re-verified through the CPA proxy.
  if [ "$cpa_proxy_validation_passed" = "1" ] && [ -n "$installer_managed_cpa_key_pending_import_copies" ]; then
    local stale_copy=""
    while IFS= read -r stale_copy; do
      [ -n "$stale_copy" ] || continue
      if ! rm -f "$stale_copy"; then
        printf '%s\n' "$(text cpa_key_cleanup_failed) File: $stale_copy" >&2
      fi
    done <<< "$installer_managed_cpa_key_pending_import_copies"
    installer_managed_cpa_key_pending_import_copies=""
  fi
}

run_docker_install() {
  if [ "$dry_run" = "1" ]; then
    say "$(text run_command): cd \"$install_dir\" && docker compose pull"
    if needs_cpa_connection_import; then
      if [ "$operation" = "upgrade" ]; then
        say "$(text run_command): cd \"$install_dir\" && docker compose stop cpa-manager-plus"
        say "$(text run_command): create a protected Manager data snapshot in ${docker_data_volume_root:-/data} before import"
      fi
      print_docker_connection_import_command
    fi
    say "$(text run_command): cd \"$install_dir\" && docker compose up -d"
    return
  fi
  if [ "$skip_execute" = "1" ]; then
    say "$(text skip_execute)"
    if needs_cpa_connection_import && [ "$operation" = "upgrade" ]; then
      say "$(text cpa_skip_upgrade)"
      print_full_upgrade_command
      return
    fi
    say "cd \"$install_dir\" && docker compose pull"
    if needs_cpa_connection_import; then
      print_docker_connection_import_command
    fi
    say "cd \"$install_dir\" && docker compose up -d"
    if needs_cpa_connection_import; then
      print_docker_post_import_validation_commands
    fi
    return
  fi
  (
    cd "$install_dir"
    docker compose pull
  )
  if needs_cpa_connection_import; then
    if [ "$operation" = "upgrade" ]; then
      if ! verify_docker_container_paths; then
        die "Configured Docker database or data key path is not readable inside the cpa-manager-plus container; refusing to migrate an unverified data set."
      fi
      cpa_connection_rollback_pending="1"
      if ! (
        cd "$install_dir"
        docker compose stop cpa-manager-plus
      ); then
        die "Failed to stop CPA Manager Plus before CPA connection import."
      fi
      if ! prepare_docker_data_snapshot; then
        die "Failed to create a protected Manager data snapshot before CPA connection import."
      fi
    fi
    if ! run_docker_connection_import; then
      die "$(text cpa_import_failed)"
    fi
    cpa_connection_imported="1"
    if [ "$operation" = "upgrade" ]; then
      if ! backup_legacy_cpa_runtime_config; then
        die "$(text cpa_import_failed)"
      fi
      legacy_cpa_runtime_config_modified="1"
      if ! remove_legacy_cpa_runtime_config; then
        die "$(text cpa_import_failed)"
      fi
    fi
  fi
  if ! (
    cd "$install_dir"
    docker compose up -d
  ); then
    die "Failed to start CPA Manager Plus after CPA connection import."
  fi
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

verify_docker_cpa_connection() {
  if [ "$cpa_connection_imported" != "1" ] && [ "$installer_managed_cpa_key_pending_cleanup" != "1" ] &&
     [ -z "$installer_managed_cpa_key_pending_import_copies" ]; then
    return 0
  fi
  (
    cd "$install_dir"
    docker compose exec -T cpa-manager-plus wget -qO- \
      --post-data='' \
      --header="Authorization: Bearer $admin_key" \
      http://127.0.0.1:18317/v0/management/cpa-connection/validate >/dev/null 2>&1
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
  else
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
  fi
  if ! verify_docker_cpa_connection; then
    die "$(text cpa_validation_failed)"
  fi
  cpa_proxy_validation_passed="1"
}

resolve_latest_version() {
  local version="$cpamp_version"
  local effective_url=""
  local resolved=""
  if [ "$version" != "latest" ]; then
    validate_version_value "$(text version)" "$version"
    printf '%s\n' "$version"
    return
  fi
  if [ "$dry_run" = "1" ]; then
    resolved="${CPAMP_VERSION_RESOLVED:-vX.Y.Z}"
    validate_version_value "$(text version)" "$resolved"
    printf '%s\n' "$resolved"
    return
  fi
  resolved="$(curl --proto '=https' --proto-redir '=https' --connect-timeout 10 --max-time 30 --max-filesize 128 -fsSL "https://raw.githubusercontent.com/${repo}/update-channel/stable-version.txt")"
  if ! printf '%s\n' "$resolved" | LC_ALL=C grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'; then
    die "Invalid stable update channel; specify CPAMP_VERSION explicitly."
  fi
  validate_version_value "$(text version)" "$resolved"
  printf '%s\n' "$resolved"
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
  mv -f "$tmp" "$file"
}

write_native_upgrade_config() {
  local binary_dir="$1"
  local file="$binary_dir/config.json"

  prepare_file "$file"
  if ! "$binary_dir/cpa-manager-plus" sanitize-runtime-config \
    --input "$native_existing_config_file" \
    --output "$file"; then
    die "Failed to sanitize the existing native runtime config."
  fi
  if grep -qiE '"(cpaUpstreamUrl|managementKeyFile)"[[:space:]]*:' "$file"; then
    die "The generated native runtime config still contains legacy CPA credentials."
  fi
}

write_native_run_script() {
  local binary_dir="$1"
  local file="$install_dir/run.sh"
  local tmp="${file}.tmp.$$"
  local runtime_package=""
  local quoted_binary_dir=""
  local quoted_config=""
  local quoted_data_dir=""
  local quoted_db_path=""
  local quoted_data_key_path=""
  local quoted_admin_key_file=""
  if [ "$dry_run" = "1" ]; then
    say "$(text write_file): $file"
    return
  fi
  prepare_file "$file"
  runtime_package="$(basename "$binary_dir")"
  validate_native_runtime_package "$runtime_package"
  printf -v quoted_binary_dir '%q' "$binary_dir"
  if [ "$operation" = "upgrade" ] && [ "$existing_install_state" = "native-managed" ]; then
    printf -v quoted_config '%q' "$binary_dir/config.json"
    printf -v quoted_data_dir '%q' "$native_data_dir"
    printf -v quoted_db_path '%q' "$native_db_path"
    printf -v quoted_data_key_path '%q' "$native_data_key_path"
    printf -v quoted_admin_key_file '%q' "$native_admin_key_file"
    cat > "$tmp" <<EOF
#!/usr/bin/env bash
set -euo pipefail
# CPAMP_RUNTIME_PACKAGE=$runtime_package
export CPA_MANAGER_CONFIG=$quoted_config
export USAGE_DATA_DIR=$quoted_data_dir
export USAGE_DB_PATH=$quoted_db_path
export CPA_MANAGER_DATA_KEY_PATH=$quoted_data_key_path
export CPA_MANAGER_ADMIN_KEY_FILE=$quoted_admin_key_file
unset CPA_UPSTREAM_URL CPA_MANAGEMENT_KEY CPA_MANAGEMENT_KEY_FILE
cd $quoted_binary_dir
exec ./cpa-manager-plus
EOF
  else
    cat > "$tmp" <<EOF
#!/usr/bin/env bash
set -euo pipefail
# CPAMP_RUNTIME_PACKAGE=$runtime_package
unset CPA_UPSTREAM_URL CPA_MANAGEMENT_KEY CPA_MANAGEMENT_KEY_FILE
cd $quoted_binary_dir
exec ./cpa-manager-plus
EOF
  fi
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
  local escaped_binary_dir=""
  local escaped_config=""
  local escaped_data_dir=""
  local escaped_db_path=""
  local escaped_data_key_path=""
  local escaped_admin_key_file=""
  if [ "$normalized_os" != "linux" ]; then
    return
  fi
  if [ "$dry_run" = "1" ]; then
    say "$(text write_file): $file"
    return
  fi
  prepare_file "$file"
  escaped_binary_dir="$(systemd_double_quote_escape "$binary_dir")"
  if [ "$operation" = "upgrade" ] && [ "$existing_install_state" = "native-managed" ]; then
    escaped_config="$(systemd_double_quote_escape "$binary_dir/config.json")"
    escaped_data_dir="$(systemd_double_quote_escape "$native_data_dir")"
    escaped_db_path="$(systemd_double_quote_escape "$native_db_path")"
    escaped_data_key_path="$(systemd_double_quote_escape "$native_data_key_path")"
    escaped_admin_key_file="$(systemd_double_quote_escape "$native_admin_key_file")"
    cat > "$tmp" <<EOF
[Unit]
Description=CPA Manager Plus
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
WorkingDirectory="$escaped_binary_dir"
Environment="CPA_MANAGER_CONFIG=$escaped_config"
Environment="USAGE_DATA_DIR=$escaped_data_dir"
Environment="USAGE_DB_PATH=$escaped_db_path"
Environment="CPA_MANAGER_DATA_KEY_PATH=$escaped_data_key_path"
Environment="CPA_MANAGER_ADMIN_KEY_FILE=$escaped_admin_key_file"
Environment="CPA_UPSTREAM_URL="
Environment="CPA_MANAGEMENT_KEY="
Environment="CPA_MANAGEMENT_KEY_FILE="
ExecStart="$escaped_binary_dir/cpa-manager-plus"
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
EOF
  else
    cat > "$tmp" <<EOF
[Unit]
Description=CPA Manager Plus
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
WorkingDirectory="$escaped_binary_dir"
Environment="CPA_UPSTREAM_URL="
Environment="CPA_MANAGEMENT_KEY="
Environment="CPA_MANAGEMENT_KEY_FILE="
ExecStart="$escaped_binary_dir/cpa-manager-plus"
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
EOF
  fi
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
  local extract_dir=""
  local extracted_binary_dir=""

  version="$(resolve_latest_version)"
  package="cpa-manager-plus_${version}_${normalized_os}_${normalized_arch}"
  archive="$install_dir/downloads/${package}.${ext}"
  asset_url="https://github.com/${repo}/releases/download/${version}/${package}.${ext}"
  binary_dir="$runtime_dir/$package"
  if [ "$operation" = "upgrade" ] && [ "$existing_install_state" = "native-managed" ] && [ -e "$binary_dir" ]; then
    binary_dir="$runtime_dir/${package}-upgrade-$(date '+%Y%m%d%H%M%S')-$$"
  fi
  native_binary_dir="$binary_dir"

  if [ "$operation" = "upgrade" ] && [ "$existing_install_state" = "native-managed" ]; then
    [ "$binary_dir" != "$native_existing_binary_dir" ] || die "The resolved native upgrade runtime matches the active runtime: $binary_dir"
  else
    preflight_native_files "$binary_dir"
  fi

  ensure_dir "$install_dir"
  ensure_dir "$install_dir/secrets"
  ensure_dir "$install_dir/data"
  ensure_dir "$install_dir/downloads"
  ensure_dir "$runtime_dir"

  if [ "$operation" != "upgrade" ] || [ "$existing_install_state" != "native-managed" ]; then
    generated_admin_key="cpamp_$(random_alnum 32)"
    admin_key="$(ensure_secret_file "$install_dir/secrets/cpamp-admin-key" "$generated_admin_key")"
    if [ "$cpa_connection_mode" = "env" ]; then
      cpa_management_key="$(ensure_secret_file "$install_dir/secrets/cpa-management-key" "$cpa_management_key")"
      cpa_management_key_file="$install_dir/secrets/cpa-management-key"
      if is_installer_owned_cpa_key_file "$cpa_management_key_file"; then
        cpa_management_key_cleanup_allowed="1"
      fi
      cpa_import_retry_state_required="1"
    fi
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
    if [ "$operation" = "upgrade" ] && [ "$existing_install_state" = "native-managed" ]; then
      extract_dir="$runtime_dir/.native-upgrade-extract-$$"
      mkdir -p "$extract_dir"
      tar -xzf "$archive" -C "$extract_dir"
      extracted_binary_dir="$extract_dir/$package"
      [ -x "$extracted_binary_dir/cpa-manager-plus" ] ||
        die "Native release archive does not contain $package/cpa-manager-plus."
      mv "$extracted_binary_dir" "$binary_dir"
      rmdir "$extract_dir" 2>/dev/null || true
    else
      tar -xzf "$archive" -C "$runtime_dir"
    fi
  else
    say "$(text skip_execute)"
    say "curl -fL \"$asset_url\" -o \"$archive\""
    say "tar -xzf \"$archive\" -C \"$runtime_dir\""
  fi

  if [ "$operation" = "upgrade" ] && [ "$existing_install_state" = "native-managed" ]; then
    write_native_upgrade_config "$binary_dir"
  else
    write_native_config "$binary_dir"
    write_native_run_script "$binary_dir"
    write_native_systemd_service "$binary_dir"
  fi
}

print_native_connection_import_command() {
  local key_file="${cpa_management_key_file:-$install_dir/secrets/cpa-management-key}"
  local db_path="${native_db_path:-$install_dir/data/usage.sqlite}"
  local data_key_path="${native_data_key_path:-$install_dir/data/data.key}"
  say "CPA_MANAGER_CONFIG=\"$native_binary_dir/config.json\" CPA_UPSTREAM_URL= CPA_MANAGEMENT_KEY= CPA_MANAGEMENT_KEY_FILE=/dev/null \"$native_binary_dir/cpa-manager-plus\" store-cpa-connection --cpa-base-url \"$cpa_url\" --management-key-file \"$key_file\" --db-path \"$db_path\" --data-key-path \"$data_key_path\""
}

print_native_post_import_validation_commands() {
  say "curl -fsS \"http://127.0.0.1:${cpamp_port}/health\""
  say "CPAMP_ADMIN_KEY=\"\$(< \"$install_dir/secrets/cpamp-admin-key\")\" && curl -fsS -H \"Authorization: Bearer \$CPAMP_ADMIN_KEY\" \"http://127.0.0.1:${cpamp_port}/status\""
  say "CPAMP_ADMIN_KEY=\"\$(< \"$install_dir/secrets/cpamp-admin-key\")\" && curl -fsS -X POST -H \"Authorization: Bearer \$CPAMP_ADMIN_KEY\" \"http://127.0.0.1:${cpamp_port}/v0/management/cpa-connection/validate\""
  if [ "$cpa_management_key_cleanup_allowed" = "1" ] && [ -n "$cpa_management_key_file" ]; then
    say "rm -f \"$cpa_management_key_file\""
  fi
}

run_native_connection_import() {
  local key_file="${cpa_management_key_file:-$install_dir/secrets/cpa-management-key}"
  local db_path="${native_db_path:-$install_dir/data/usage.sqlite}"
  local data_key_path="${native_data_key_path:-$install_dir/data/data.key}"
  if ! needs_cpa_connection_import; then
    return
  fi
  if [ "$dry_run" = "1" ]; then
    say "$(text run_command):"
    print_native_connection_import_command
    return
  fi
  if [ "$skip_execute" = "1" ]; then
    say "$(text skip_execute)"
    print_native_connection_import_command
    return
  fi
  materialize_cpa_management_key_file
  persist_cpa_import_retry_state
  if [ "$operation" = "upgrade" ] && [ "$existing_install_state" = "native-managed" ]; then
    native_upgrade_data_mutated="1"
  fi
  if ! CPA_MANAGER_CONFIG="$native_binary_dir/config.json" \
    CPA_UPSTREAM_URL= \
    CPA_MANAGEMENT_KEY= \
    CPA_MANAGEMENT_KEY_FILE=/dev/null \
    "$native_binary_dir/cpa-manager-plus" store-cpa-connection \
    --cpa-base-url "$cpa_url" \
    --management-key-file "$key_file" \
    --db-path "$db_path" \
    --data-key-path "$data_key_path"; then
    die "$(text cpa_import_failed)"
  fi
  cpa_connection_imported="1"
}

print_native_upgrade_plan() {
  local preview_version="$cpamp_version"
  local package=""
  if [ "$preview_version" = "latest" ]; then
    preview_version="${CPAMP_VERSION_RESOLVED:-vX.Y.Z}"
  fi
  package="cpa-manager-plus_${preview_version}_${normalized_os}_${normalized_arch}"
  native_binary_dir="$install_dir/runtime/$package"
  say "$(text native_upgrade_pending)"
  say "$(text run_command): curl -fL \"https://github.com/${repo}/releases/download/${preview_version}/${package}.tar.gz\" -o \"$install_dir/downloads/${package}.tar.gz\""
  if needs_cpa_connection_import; then
    print_native_connection_import_command
  fi
  say "$(text run_command): replace \"$install_dir/run.sh\" only after the offline import succeeds"
  say "$(text run_command): nohup \"$install_dir/run.sh\" >> \"$install_dir/cpa-manager-plus.log\" 2>&1 &"
  print_native_post_import_validation_commands
  print_full_upgrade_command
}

stop_native_pid() {
  local pid="$1"
  local attempts="${CPAMP_NATIVE_STOP_ATTEMPTS:-40}"
  local i=1
  case "$pid" in
    ''|*[!0-9]*) return 0 ;;
  esac
  case "$attempts" in
    ''|*[!0-9]*) attempts=40 ;;
  esac
  if ! kill -0 "$pid" >/dev/null 2>&1; then
    return 0
  fi
  kill -TERM "$pid" >/dev/null 2>&1 || return 1
  while [ "$i" -le "$attempts" ]; do
    if ! kill -0 "$pid" >/dev/null 2>&1 ||
       [[ "$(ps -p "$pid" -o stat= 2>/dev/null || true)" =~ ^[[:space:]]*Z ]]; then
      return 0
    fi
    sleep 0.25
    i=$((i + 1))
  done
  return 1
}

native_port_listener_state() {
  local port="$1"
  if command_exists lsof; then
    if lsof -nP -a -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
      return 0
    fi
    return 1
  fi
  if command_exists ss; then
    if ss -H -ltn 2>/dev/null | awk -v port=":$port" '$4 == port || $4 ~ port"$" { found = 1 } END { exit found ? 0 : 1 }'; then
      return 0
    fi
    return 1
  fi
  if command_exists netstat; then
    if netstat -an 2>/dev/null | awk -v port="$port" '$0 ~ /LISTEN/ && (index($0, ":" port) > 0 || index($0, "." port) > 0) { found = 1 } END { exit found ? 0 : 1 }'; then
      return 0
    fi
    return 1
  fi
  return 2
}

ensure_native_upgrade_port_is_free() {
  local state=0
  if native_port_listener_state "$cpamp_port"; then
    die "Cannot safely migrate native CPA configuration while port $cpamp_port is listening without an owned PID file. Stop the service and retry."
  else
    state=$?
  fi
  if [ "$state" -eq 2 ]; then
    die "Cannot verify that native port $cpamp_port is free because no listener inspection command is available. Stop the service and retry with lsof, ss, or netstat installed."
  fi
}

# native_upgrade_owns_pid reports whether the pid file still names the exact
# process this run started. Comparing the recorded process start time makes the
# decision independent of how far the child has progressed: cwd or command-line
# heuristics can observe the child before it has even changed into the runtime
# directory, which would wrongly veto the data rollback.
native_spawned_pid_start_matches() {
  local pid="$1"
  [ -n "$native_spawned_pid_start" ] || return 1
  [ "$(ps -p "$pid" -o lstart= 2>/dev/null || true)" = "$native_spawned_pid_start" ]
}

native_upgrade_owns_pid() {
  local pid="$1"
  case "$pid" in
    ''|*[!0-9]*) return 1 ;;
  esac
  if [ -z "$native_spawned_pid" ] || [ "$native_spawned_pid" != "$pid" ]; then
    return 1
  fi
  native_spawned_pid_start_matches "$pid"
}

native_pid_matches_binary_dir() {
  local pid="$1"
  local binary_dir="$2"
  local command_line=""
  local expected_binary=""
  local process_cwd=""
  local process_files=""
  local resolved_binary_dir=""
  case "$pid" in
    ''|*[!0-9]*) return 1 ;;
  esac
  # Once this run has recorded a PID, a later start-time mismatch must not be
  # bypassed by the binary/cwd fallback: the PID may have been reused by an
  # unrelated process that happens to execute the same runtime binary.
  if [ "$pid" = "$native_spawned_pid" ]; then
    native_spawned_pid_start_matches "$pid" || return 1
  fi
  resolved_binary_dir="$(cd "$binary_dir" 2>/dev/null && pwd -P)" || return 1
  expected_binary="$resolved_binary_dir/cpa-manager-plus"
  if [ -r "/proc/$pid/exe" ]; then
    [ "$(readlink "/proc/$pid/exe" 2>/dev/null || true)" = "$expected_binary" ] && return 0
  fi
  if [ -r "/proc/$pid/cwd" ]; then
    process_cwd="$(readlink "/proc/$pid/cwd" 2>/dev/null || true)"
  elif command_exists lsof; then
    process_cwd="$(lsof -a -p "$pid" -d cwd -Fn 2>/dev/null | awk 'substr($0, 1, 1) == "n" { print substr($0, 2); exit }')"
  fi
  # A readable but different working directory proves the pid is not ours. An
  # unreadable cwd (right after exec, or when lsof cannot resolve it yet) must
  # not veto the rollback; the command-line check below still has to identify
  # our binary before the pid is trusted.
  if [ -n "$process_cwd" ] && [ "$process_cwd" != "$resolved_binary_dir" ]; then
    return 1
  fi

  if [ -r "/proc/$pid/cmdline" ]; then
    command_line="$(tr '\000' ' ' < "/proc/$pid/cmdline" 2>/dev/null || true)"
  elif command_exists ps; then
    command_line="$(ps -p "$pid" -o command= 2>/dev/null || true)"
  fi
  if [ -n "$command_line" ]; then
    case "$command_line" in
      "$expected_binary"|"$expected_binary "*|*" $expected_binary"|*" $expected_binary "*) return 0 ;;
      "./cpa-manager-plus"|"./cpa-manager-plus "*|*" ./cpa-manager-plus"|*" ./cpa-manager-plus "*) return 0 ;;
    esac
  fi

  if command_exists lsof; then
    process_files="$(lsof -a -p "$pid" -Fn 2>/dev/null || true)"
    printf '%s\n' "$process_files" | grep -Fqx "n$expected_binary" && return 0
  fi
  return 1
}

backup_native_upgrade_runtime_entry() {
  local timestamp=""
  timestamp="$(date '+%Y%m%d-%H%M%S')"
  native_upgrade_backup_dir="$install_dir/backups/native-upgrade-$timestamp-$$"
  mkdir -p "$native_upgrade_backup_dir"
  if [ -f "$install_dir/run.sh" ]; then
    native_upgrade_run_script_existed="1"
    cp -p "$install_dir/run.sh" "$native_upgrade_backup_dir/run.sh"
  fi
  if [ -f "$install_dir/cpa-manager-plus.service" ]; then
    native_upgrade_service_existed="1"
    cp -p "$install_dir/cpa-manager-plus.service" "$native_upgrade_backup_dir/cpa-manager-plus.service"
  fi
}

stop_existing_native_process() {
  local pid_file="$install_dir/cpa-manager-plus.pid"
  local pid=""
  if [ ! -f "$pid_file" ]; then
    ensure_native_upgrade_port_is_free
    return
  fi
  pid="$(< "$pid_file")"
  pid="${pid%$'\r'}"
  case "$pid" in
    ''|*[!0-9]*) die "Invalid native PID file: $pid_file" ;;
  esac
  if kill -0 "$pid" >/dev/null 2>&1; then
    native_pid_matches_binary_dir "$pid" "$native_existing_binary_dir" ||
      die "Native PID file does not belong to this CPA Manager Plus installation (PID $pid)."
    native_previous_pid="$pid"
    stop_native_pid "$pid" || die "Failed to stop the existing native CPA Manager Plus process (PID $pid)."
    native_previous_process_was_running="1"
  fi
  rm -f "$pid_file"
  ensure_native_upgrade_port_is_free
}

prepare_native_upgrade_rollback() {
  native_upgrade_rollback_pending="1"
  backup_native_upgrade_runtime_entry
  stop_existing_native_process

  native_upgrade_snapshot_dir="$native_upgrade_backup_dir/manager-data-snapshot"
  if ! run_native_data_snapshot create; then
    die "Failed to create a protected Manager data snapshot before native CPA connection import."
  fi
  native_upgrade_snapshot_created="1"
}

restore_native_upgrade_file() {
  local existed="$1"
  local backup="$2"
  local target="$3"
  if [ "$existed" = "1" ]; then
    mkdir -p "$(dirname "$target")" || return 1
    cp -p "$backup" "$target"
  else
    rm -f "$target"
  fi
}

run_native_data_snapshot() {
  local action="$1"
  local binary="${native_binary_dir:-$native_existing_binary_dir}/cpa-manager-plus"
  [ -n "$native_upgrade_snapshot_dir" ] || return 1
  if [ "$action" = "delete" ]; then
    "$binary" manager-data-snapshot delete \
      --snapshot-dir "$native_upgrade_snapshot_dir"
  else
    "$binary" manager-data-snapshot "$action" \
      --snapshot-dir "$native_upgrade_snapshot_dir" \
      --db-path "$native_db_path" \
      --data-key-path "$native_data_key_path"
  fi
}

restore_native_upgrade_runtime_entry() {
  restore_native_upgrade_file "$native_upgrade_run_script_existed" "$native_upgrade_backup_dir/run.sh" "$install_dir/run.sh" || return 1
  restore_native_upgrade_file "$native_upgrade_service_existed" "$native_upgrade_backup_dir/cpa-manager-plus.service" "$install_dir/cpa-manager-plus.service" || return 1
}

restart_previous_native_process() {
  local log_file="$install_dir/cpa-manager-plus.log"
  local pid_file="$install_dir/cpa-manager-plus.pid"
  local pid=""
  [ "$native_previous_process_was_running" = "1" ] || return 0
  nohup "$install_dir/run.sh" >> "$log_file" 2>&1 &
  pid="$!"
  printf '%s\n' "$pid" > "$pid_file"
  sleep 0.1
  kill -0 "$pid" >/dev/null 2>&1
}

rollback_native_upgrade() {
  local failed="0"
  local process_safe="1"
  local data_restore_succeeded="1"
  local runtime_restore_succeeded="1"
  local pid_file="$install_dir/cpa-manager-plus.pid"
  local current_pid=""
  native_upgrade_rollback_pending="0"

  # The PID file is only a publication record. If publishing it failed after
  # the new process was spawned, native_spawned_pid is the only trustworthy
  # handle left for stopping this run's process. Never restore data and start
  # the previous runtime while that process is still alive.
  if [ "$native_upgrade_switch_applied" = "1" ]; then
    if [ -f "$pid_file" ]; then
      current_pid="$(< "$pid_file")"
      current_pid="${current_pid%$'\r'}"
    else
      current_pid="$native_spawned_pid"
    fi
    if [ -z "$current_pid" ] && [ -n "$native_spawned_pid" ]; then
      current_pid="$native_spawned_pid"
    fi
    case "$current_pid" in
      '') ;;
      *[!0-9]*)
        if [ -n "$native_spawned_pid" ]; then
          current_pid="$native_spawned_pid"
        else
          failed="1"
          process_safe="0"
        fi
        ;;
      *)
        if [ -n "$native_spawned_pid" ] && [ "$current_pid" != "$native_spawned_pid" ]; then
          failed="1"
          process_safe="0"
        fi
        ;;
    esac
    if [ "$process_safe" = "1" ] && [ -n "$current_pid" ]; then
      case "$current_pid" in
        ''|*[!0-9]*)
          failed="1"
          process_safe="0"
          ;;
        *)
          if kill -0 "$current_pid" >/dev/null 2>&1; then
            if native_upgrade_owns_pid "$current_pid" ||
               native_pid_matches_binary_dir "$current_pid" "$native_binary_dir"; then
              if stop_native_pid "$current_pid"; then
                rm -f "$pid_file"
              else
                failed="1"
                process_safe="0"
              fi
            else
              failed="1"
              process_safe="0"
            fi
          else
            rm -f "$pid_file"
          fi
          ;;
      esac
    fi
  fi
  if [ "$process_safe" != "1" ]; then
    printf '%s\n' "$(text native_rollback_failed) Backup: $native_upgrade_backup_dir" >&2
    return 1
  fi
  if [ "$native_upgrade_data_mutated" = "1" ] && [ "$native_upgrade_snapshot_created" = "1" ]; then
    if ! run_native_data_snapshot restore; then
      data_restore_succeeded="0"
      failed="1"
    fi
  fi
  if [ "$native_upgrade_switch_applied" = "1" ]; then
    if ! restore_native_upgrade_runtime_entry; then
      runtime_restore_succeeded="0"
      failed="1"
    fi
  fi
  # Never start the previous runtime on a data or entry set whose restoration
  # was not confirmed. The snapshot and runtime-entry backup remain available
  # for manual recovery in that case.
  if [ "$data_restore_succeeded" = "1" ] && [ "$runtime_restore_succeeded" = "1" ]; then
    restart_previous_native_process || failed="1"
  fi
  if [ "$failed" = "1" ]; then
    printf '%s\n' "$(text native_rollback_failed) Backup: $native_upgrade_backup_dir" >&2
    return 1
  fi

  # Business rollback is complete. Snapshot deletion is cleanup only; a
  # failure keeps the complete file-set artifact and must not turn rollback
  # into a second failure path.
  if [ "$native_upgrade_snapshot_created" = "1" ]; then
    if ! run_native_data_snapshot delete; then
      printf '%s\n' "$(text cpa_snapshot_cleanup_failed) Snapshot: $native_upgrade_snapshot_dir" >&2
    else
      native_upgrade_snapshot_created="0"
    fi
  fi
}

switch_native_runtime_entry() {
  native_upgrade_switch_applied="1"
  write_native_run_script "$native_binary_dir"
  write_native_systemd_service "$native_binary_dir"
}

commit_native_upgrade() {
  native_upgrade_rollback_pending="0"
  if [ "$native_upgrade_snapshot_created" = "1" ]; then
    if ! run_native_data_snapshot delete; then
      printf '%s\n' "$(text cpa_snapshot_cleanup_failed) Snapshot: $native_upgrade_snapshot_dir" >&2
    else
      native_upgrade_snapshot_created="0"
    fi
  fi
}

run_native_upgrade() {
  generate_native_files
  prepare_native_upgrade_rollback
  run_native_connection_import
  switch_native_runtime_entry
  native_upgrade_data_mutated="1"
  run_native_install
  commit_native_upgrade
  finalize_cpa_connection_import
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
  local attempts="${CPAMP_NATIVE_HEALTH_ATTEMPTS:-20}"
  local i=1

  case "$attempts" in
    ''|*[!0-9]*) die "CPAMP_NATIVE_HEALTH_ATTEMPTS must be a positive integer." ;;
  esac
  [ "$attempts" -ge 1 ] || die "CPAMP_NATIVE_HEALTH_ATTEMPTS must be a positive integer."

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

verify_native_admin_key() {
  [ -n "$admin_key" ] || return 1
  curl -fsS \
    -H "Authorization: Bearer $admin_key" \
    "http://127.0.0.1:${cpamp_port}/status" >/dev/null 2>&1
}

verify_native_cpa_connection() {
  if [ "$cpa_connection_imported" != "1" ] && [ "$installer_managed_cpa_key_pending_cleanup" != "1" ] &&
     [ -z "$installer_managed_cpa_key_pending_import_copies" ]; then
    return 0
  fi
  curl -fsS -X POST \
    -H "Authorization: Bearer $admin_key" \
    "http://127.0.0.1:${cpamp_port}/v0/management/cpa-connection/validate" >/dev/null 2>&1
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
    say "nohup \"$install_dir/run.sh\" >> \"$log_file\" 2>&1 &"
    if needs_cpa_connection_import; then
      print_native_post_import_validation_commands
    fi
    return
  fi
  if [ -f "$pid_file" ] && kill -0 "$(cat "$pid_file")" >/dev/null 2>&1; then
    pid="$(cat "$pid_file")"
    say "CPAMP is already running with PID $pid."
  else
    nohup "$install_dir/run.sh" >> "$log_file" 2>&1 &
    pid="$!"
    native_spawned_pid="$pid"
    native_spawned_pid_start="$(ps -p "$pid" -o lstart= 2>/dev/null || true)"
    printf '%s\n' "$pid" > "$pid_file"
  fi
  if [ -n "$native_spawned_pid" ] && [ "$native_spawned_pid" != "$pid" ]; then
    native_spawned_pid="$pid"
    native_spawned_pid_start="$(ps -p "$pid" -o lstart= 2>/dev/null || true)"
  fi
  wait_native_health "$pid" "$log_file"
  if ! verify_native_admin_key; then
    die "$(text auth_failed)"
  fi
  auth_validation_status="verified"
  if ! verify_native_cpa_connection; then
    die "$(text cpa_validation_failed)"
  fi
  cpa_proxy_validation_passed="1"
}

post_install_message() {
  local reveal=""
  local effective_admin_key_file="${native_admin_key_file:-$install_dir/secrets/cpamp-admin-key}"
  local effective_cpa_key_file="${cpa_management_key_file:-$install_dir/secrets/cpa-management-key}"
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
    say "$(text admin_key_file): $effective_admin_key_file"
  else
    say "$(text key_saved): $effective_admin_key_file"
    say "$(text key_view_command): cat \"$effective_admin_key_file\""
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
    if [ "$cpa_connection_imported" = "1" ] || [ "$cpa_connection_mode" = "stored" ]; then
      say "$(text cpa_imported)"
    else
      say "$(text cpa_temp_key_file): $effective_cpa_key_file"
    fi
    say "$(text demo_client_key_file): $install_dir/secrets/cpa-demo-client-key"
    if [ "$dry_run" != "1" ] && [ "$skip_execute" != "1" ]; then
      say "$(text next_full_stack)"
    fi
  elif [ "$cpa_connection_mode" = "setup" ]; then
    if [ "$dry_run" != "1" ] && [ "$skip_execute" != "1" ]; then
      say "$(text next_setup)"
    fi
  else
    if [ "$cpa_connection_imported" = "1" ] || [ "$cpa_connection_mode" = "stored" ]; then
      say "$(text cpa_imported)"
      if [ "$cpa_connection_imported" = "1" ] && [ "$cpa_management_key_cleanup_allowed" != "1" ] && [ -n "$cpa_management_key_file" ]; then
        say "$(text cpa_key_file): $effective_cpa_key_file"
      fi
    else
      say "$(text cpa_temp_key_file): $effective_cpa_key_file"
    fi
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

  if [ "$operation" = "upgrade" ] && [ "$existing_install_state" = "native-managed" ]; then
    load_existing_native_config
    print_summary
    check_requirements
    if [ "$dry_run" = "1" ] || [ "$skip_execute" = "1" ]; then
      auth_validation_status="skipped"
      print_native_upgrade_plan
      post_install_message
      return
    fi
    run_native_upgrade
    post_install_message
    return
  fi

  if { [ "$operation" = "upgrade" ] && [ "$existing_install_state" = "managed" ]; } ||
     { [ "$operation" = "repair" ] && [ "$existing_install_state" = "managed" ]; }; then
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
    commit_legacy_cpa_runtime_config
    finalize_cpa_connection_import
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
    commit_legacy_cpa_runtime_config
    finalize_cpa_connection_import
  else
    backup_generated_config
    generate_native_files
    run_native_connection_import
    run_native_install
    finalize_cpa_connection_import
  fi

  post_install_message
}

main "$@"
