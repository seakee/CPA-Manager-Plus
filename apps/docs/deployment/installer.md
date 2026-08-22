# 一键安装脚本

安装脚本适合第一次部署，或已经有 CPA、只想把 CPAMP 跑起来的环境。它不会直接覆盖已有配置文件；执行前会先展示安装摘要，确认后才写入文件和启动服务。

当安装器收到 CPA URL 和 CPA Management Key 时，这些值只作为一次性导入输入：安装器调用 `store-cpa-connection`，使用 `data.key` 将连接写入 CPAMP 的 SQLite 配置，然后在健康检查、管理员鉴权和 CPA 管理接口代理验证成功后删除临时密钥文件。最终运行配置不再包含 `CPA_UPSTREAM_URL`、`CPA_MANAGEMENT_KEY_FILE` 或长期 CPA Key secret mount。

大多数用户只需要完成四步：运行脚本、选择安装范围、选择 Docker 或原生包、确认摘要。安装完成后按脚本输出的地址和密钥登录即可。

## 运行方式

下载脚本后运行：

```bash
curl -fsSLO https://raw.githubusercontent.com/seakee/CPA-Manager-Plus/main/bin/install-cpamp.sh
bash install-cpamp.sh
```

如果需要先查看内容：

```bash
less install-cpamp.sh
bash install-cpamp.sh
```

脚本会按顺序处理：

1. 检查系统、架构、WSL、端口和必要命令。
2. 选择后续操作语言。
3. 选择安装范围：CPA + CPAMP，或仅安装 CPAMP。
4. 选择部署方式：Docker，或 CPAMP 原生包。
5. 生成最小配置文件和本机 secret 文件（CPA Management Key 文件仅在导入期间临时使用）。
6. 展示摘要，可确认、返回修改或退出。
7. 确认后执行部署。

## 支持的组合

| 安装范围    | Docker |   原生包 |
| ----------- | -----: | -------: |
| CPA + CPAMP |   支持 | 暂不支持 |
| 仅 CPAMP    |   支持 |     支持 |

完整安装推荐 Docker。CPAMP 原生包只包含 Manager Server，不包含 CPA 运行时；如果要用原生包，需要先单独部署 CPA。

## 完整 Docker 安装

没有现成 CPA 时选择这个组合。安装器会同时启动 CPA 和 CPAMP，并准备持久化目录和登录密钥。

::: details 查看安装器生成的文件和连接方式

选择 CPA + CPAMP 后，脚本会生成：

```text
compose.yaml
.env
secrets/cpamp-admin-key
secrets/cpa-management-key       # CPA 连接导入期间的临时文件
secrets/cpa-demo-client-key
cliproxyapi/config.yaml
cliproxyapi/auths/
cliproxyapi/logs/
```

默认生成的密钥格式如下：

```text
CPAMP 管理员密钥: cpamp_ + 32 位字母数字
CPA Management Key: cpa_ + 32 位字母数字
演示客户端 API Key: sk- + 64 位字母数字
```

重跑脚本时，已有的非空单行 secret 文件会被原样复用；手动管理的密钥不需要符合默认生成格式。

CPA 最小配置会启用远程管理和用量发布：

```yaml
api-keys:
  - 'sk-...'

remote-management:
  secret-key: 'cpa_...'
  allow-remote: true

usage-statistics-enabled: true
redis-usage-queue-retention-seconds: 60
```

生成的 Compose 会按 CPA 镜像的实际工作目录挂载：

```text
./cliproxyapi/config.yaml -> /CLIProxyAPI/config.yaml
./cliproxyapi/auths       -> /root/.cli-proxy-api
./cliproxyapi/logs        -> /CLIProxyAPI/logs
```

CPA 启动时会把明文 `remote-management.secret-key` 自动写回为 bcrypt hash，所以 `cliproxyapi/config.yaml` 需要保持可写。

安装器会在 CPAMP 容器启动前执行一次离线导入，并使用 Docker 内网地址：

```text
http://cli-proxy-api:8317
```

导入命令把 CPA URL 和 CPA Management Key 加密写入 `/data/usage.sqlite`，密钥由 `/data/data.key` 保护。安装成功且健康、管理员鉴权及 CPA 管理接口代理验证通过后，`secrets/cpa-management-key` 会被删除；最终 `compose.yaml` 不再向 Manager Server 传入 CPA Key。打开面板后直接使用 CPAMP 管理员密钥登录，不需要再走首次 setup。

部署完成后打开：

```text
http://<host>:18317/management.html
```

脚本会保存 CPAMP 管理员密钥并打印文件路径和查看命令。交互安装可以选择是否立即在终端显示完整密钥；不要分享包含密钥的终端截图。演示客户端 API Key 只用于安装后快速连通性验证，生产客户端建议在面板里重新创建并按用途命名。

:::

## 仅安装 CPAMP

如果 CPA 已经在运行，选择仅安装 CPAMP。交互向导会优先询问是否现在填写 CPA URL 和 CPA Management Key。

选择“现在填写并跳过首次 setup”后，脚本会把连接一次性导入 SQLite：

```text
.env                    # 仅保留非敏感运行配置
data/usage.sqlite       # 加密后的 Manager 配置
data/data.key
```

启动后直接使用 CPAMP 管理员密钥登录，不需要再在面板里填写首次 setup。导入成功后，CPA URL 和 CPA Management Key 由 CPAMP 服务端从加密 SQLite 读取；面板不会把已保存的 CPA Key 返回浏览器。需要调整连接时，在面板中提交新的 CPA Key；离线导入命令只接受首次导入或完全一致的幂等重试，不会覆盖冲突连接。

如果选择稍后填写，脚本不会把 CPA Management Key 写入环境文件；打开面板后，在 setup 中填写：

```text
CPA URL
CPA Management Key
请求监控偏好
```

手动部署仍可使用 `CPA_UPSTREAM_URL`、`CPA_MANAGEMENT_KEY` 或 `CPA_MANAGEMENT_KEY_FILE` 管理连接，但这不是一键安装器的最终配置；安装器会把它们迁移到加密 SQLite。使用 `CPAMP_SKIP_EXECUTE=1` 时不会执行导入或删除临时文件，脚本会打印导入和清理命令，便于人工复核。

Docker 方式仅安装 CPAMP 时，如果 CPA 跑在同一台宿主机上，脚本默认使用：

```text
http://host.docker.internal:8317
```

Linux 上会同时写入 `host.docker.internal:host-gateway`，让容器能访问宿主机上的 CPA。CPA 跑在其他机器时，把 CPA URL 改成对应地址即可。

## 原生包模式

仅安装 CPAMP 时可以选择原生包。脚本会按系统和架构下载 GitHub Release 中的包，生成：

```text
runtime/<package>/
data/
secrets/cpamp-admin-key
secrets/cpa-management-key       # 导入完成后删除
run.sh
cpa-manager-plus.service  # Linux
cpa-manager-plus.log
cpa-manager-plus.pid
```

原生包会先离线导入 CPA 连接，再以前台程序的方式启动到后台。安装器会依次验证健康接口、管理员鉴权和通过 CPAMP 代理访问 CPA 管理配置，全部通过后才删除临时 `secrets/cpa-management-key`。Linux 会额外生成 `cpa-manager-plus.service`，可复制到 systemd 服务目录后按你的系统策略启用；macOS 或已有进程管理方式可以继续参考 `run.sh`。

::: details 自动化部署、重跑和修复

## 高级用法

只看计划，不写文件、不启动服务：

```bash
CPAMP_DRY_RUN=1 bash install-cpamp.sh
```

生成配置但不启动：

```bash
CPAMP_SKIP_EXECUTE=1 bash install-cpamp.sh
```

此模式会保留临时 CPA Key，并按“导入 -> 启动 -> 健康检查 -> 管理员鉴权 -> CPA 管理接口代理验证 -> 删除临时 Key”的顺序打印人工命令。对于仍使用旧 env/secret 的 Docker 升级，脚本不会提前修改运行配置，而是打印一条去掉 `CPAMP_SKIP_EXECUTE` 的完整升级命令，由正常升级流程负责备份、回滚和最终清理。

非交互完整 Docker 安装示例：

```bash
CPAMP_NON_INTERACTIVE=1 \
CPAMP_CONFIRM=1 \
CPAMP_LANG=zh-CN \
CPAMP_INSTALL_MODE=stack \
CPAMP_DEPLOY_METHOD=docker \
CPAMP_INSTALL_DIR="$HOME/cpa-manager-plus" \
bash install-cpamp.sh
```

常用变量：

| 变量                        | 说明                                                                               |
| --------------------------- | ---------------------------------------------------------------------------------- |
| `CPAMP_LANG`                | `zh-CN` 或 `en-US`。                                                               |
| `CPAMP_INSTALL_MODE`        | `stack` 或 `cpamp`。                                                               |
| `CPAMP_DEPLOY_METHOD`       | `docker` 或 `native`。                                                             |
| `CPAMP_INSTALL_DIR`         | 安装目录，默认 `~/cpa-manager-plus`。                                              |
| `CPAMP_PORT`                | CPAMP 对外端口，默认 `18317`。                                                     |
| `CPAMP_CPA_PORT`            | 完整 Docker 安装时 CPA 对外端口，默认 `8317`。                                     |
| `CPAMP_IMAGE`               | CPAMP Docker 镜像。                                                                |
| `CPAMP_CPA_IMAGE`           | CPA Docker 镜像。                                                                  |
| `CPAMP_VERSION`             | 原生包版本，默认 `latest`。                                                        |
| `CPAMP_CPA_CONNECTION_MODE` | `setup` 或 `env`；`env` 只表示从环境/提示读取一次性导入输入。                      |
| `CPAMP_CPA_URL`             | `env` 模式下的 CPA URL。                                                           |
| `CPAMP_CPA_MANAGEMENT_KEY`  | `env` 模式下的一次性 CPA Management Key 输入，不写入最终 Compose 配置。             |
| `CPAMP_OPERATION`           | `install`、`upgrade`、`repair` 或 `regenerate`。已有部署的非交互操作必须明确设置。 |
| `CPAMP_PROJECT_NAME`        | Docker Compose 项目名，默认 `cpamp`；需要在同一主机创建隔离的新部署时使用。        |

## 重跑和覆盖

以下 `CPAMP_OPERATION` 操作模式用于 Docker 部署；原生包继续使用原有的版本和覆盖参数。

脚本会在写文件前检查安装目录和 Docker 数据卷。检测到已有部署时，交互模式会提供：

1. **升级现有部署**：拉取镜像并更新容器；检测到旧的 env/secret CPA 连接时，会先导入 SQLite，再定向移除旧运行时字段。
2. **修复管理员登录**：停止 CPAMP，把 SQLite 中的管理员凭证同步为 `secrets/cpamp-admin-key`，然后重启并验证登录；CPA 服务和业务数据不会被删除。
3. **重新生成配置**：备份现有生成配置后重新写入，继续复用 secret 和数据卷。
4. **退出**。

如果安装目录已经被删除、但 `cpamp_cpa-manager-plus-data` 仍然存在，脚本不会再静默创建新密钥并报告成功，而是要求恢复旧数据或使用新的 Compose 项目名进行全新安装。

非交互升级：

```bash
CPAMP_OPERATION=upgrade \
CPAMP_NON_INTERACTIVE=1 \
CPAMP_CONFIRM=1 \
bash install-cpamp.sh
```

非交互修复管理员登录：

```bash
CPAMP_OPERATION=repair \
CPAMP_NON_INTERACTIVE=1 \
CPAMP_CONFIRM=1 \
bash install-cpamp.sh
```

如果安装目录已经丢失、只剩旧 Docker 数据卷，非交互修复还必须设置原来的 `CPAMP_INSTALL_MODE=stack` 或 `CPAMP_INSTALL_MODE=cpamp`，避免生成错误的服务组合。

如果确定要重新生成配置：

```bash
CPAMP_OPERATION=regenerate bash install-cpamp.sh
```

`CPAMP_OVERWRITE=1` 继续兼容旧用法，并会映射到配置重新生成流程。脚本会把旧的 `.env`、`compose.yaml`、CPA 配置、`run.sh` 和 service 文件备份到安装目录的 `backups/installer-*`，但仍建议单独备份 `secrets/`、`data/` 和 `cliproxyapi/`。丢失 `data.key` 后，已保存的 CPA Management Key 无法恢复。

升级旧版 env/secret Docker 部署时，脚本会先停止 Manager Server，并在数据卷内创建权限受限的离线快照。快照同时包含 `usage.sqlite`、`-wal`、`-shm`、`-journal` 和 `data.key` 的原始存在状态与内容；只有快照完整创建后才会执行导入或触发可能的 SQLite schema 迁移。健康、管理员鉴权和 CPA 代理验证全部通过后才删除快照。如果中途失败，脚本会先停止新进程、恢复数据与旧配置，再启动旧服务；恢复失败时会保留快照路径供人工处理。

脚本还会保留带时间戳的 `compose.yaml.cpa-key-migration.bak.*` 和 `.env.cpa-key-migration.bak.*`。旧 CPA 输入只从 `cpa-manager-plus` Compose 配置实际引用的来源中解析，当前进程环境覆盖 `.env`；未引用的旧 `.env` 声明或遗留 secret 文件不会触发迁移。安装器管理的 `secrets/cpa-management-key` 可以被收紧权限并在成功后删除；外部 `CPA_MANAGEMENT_KEY_FILE` 只读使用，其内容、权限和文件本身均保持不变。

原生包升级同样会在切换运行入口前备份 SQLite 伴随文件和 `data.key`。旧 `config.json` 由新二进制使用 Go JSON 解析器清理，只删除 `cpaUpstreamUrl` 与 `managementKeyFile` 并保留未知字段；任何未捕获的 shell 退出也会触发统一回滚。

:::

## 启动和登录验证

Docker 安装、升级或修复，以及原生包安装后，脚本都会等待 CPAMP 健康检查通过，再使用当前管理员密钥请求受保护的 Manager Server 接口。对于本次导入的 CPA 连接，脚本还会通过 CPAMP 服务端代理请求 CPA 管理配置，确认 CPA URL 和 Key 实际可用。全部验证成功后才会输出“安装步骤已完成”，并清理本次导入用的临时 CPA Key 文件。

如果导入、健康检查、管理员密钥验证或 CPA 连接代理验证失败，临时 CPA Key 文件会保留以便重试；旧版升级还会恢复原配置。容器已启动但管理员密钥验证失败时，交互模式会询问是否自动停止 CPAMP 并修复管理员凭证；非交互模式会返回失败状态，并提示使用 `CPAMP_OPERATION=repair`。这可以避免用户拿到一个与旧数据库不匹配的“新密钥”，也避免错误 CPA Key 在未验证时被清理。
