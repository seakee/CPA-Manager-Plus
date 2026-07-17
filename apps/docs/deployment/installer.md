# 一键安装脚本

安装脚本适合第一次部署，或已经有 CPA、只想把 CPAMP 跑起来的环境。它不会直接覆盖已有配置文件；执行前会先展示安装摘要，确认后才写入文件和启动服务。

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
5. 生成最小配置文件和本机 secret 文件。
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
secrets/cpa-management-key
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

CPAMP 会通过 Docker secret 读取 CPA Management Key，并使用 Docker 内网地址：

```text
http://cli-proxy-api:8317
```

这组连接由安装目录中的 `compose.yaml` 和 `secrets/cpa-management-key` 管理。打开面板后直接使用 CPAMP 管理员密钥登录，不需要再走首次 setup。

部署完成后打开：

```text
http://<host>:18317/management.html
```

脚本会保存 CPAMP 管理员密钥并打印文件路径和查看命令。交互安装可以选择是否立即在终端显示完整密钥；不要分享包含密钥的终端截图。演示客户端 API Key 只用于安装后快速连通性验证，生产客户端建议在面板里重新创建并按用途命名。

:::

## 仅安装 CPAMP

如果 CPA 已经在运行，选择仅安装 CPAMP。交互向导会优先询问是否现在填写 CPA URL 和 CPA Management Key。

选择“现在填写并跳过首次 setup”后，脚本会把连接写入安装目录：

```text
.env
secrets/cpa-management-key
```

启动后直接使用 CPAMP 管理员密钥登录，不需要再在面板里填写首次 setup。这个模式是环境管理配置：CPA URL 和 CPA Management Key 来自安装目录，面板不能直接改写这组连接；需要调整时，更新安装目录中的配置和 secret 后重启 CPAMP。

如果选择稍后填写，脚本不会把 CPA Management Key 写入环境文件；打开面板后，在 setup 中填写：

```text
CPA URL
CPA Management Key
请求监控偏好
```

如果想让连接配置由环境管理，可以在脚本里选择“写入本机 secret 文件并由环境管理”。这种模式下，CPA URL 和 CPA Management Key 来自配置文件，面板不能直接改写这组连接。

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
run.sh
cpa-manager-plus.service  # Linux
cpa-manager-plus.log
cpa-manager-plus.pid
```

原生包会以前台程序的方式启动到后台。Linux 会额外生成 `cpa-manager-plus.service`，可复制到 systemd 服务目录后按你的系统策略启用；macOS 或已有进程管理方式可以继续参考 `run.sh`。

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
| `CPAMP_CPA_CONNECTION_MODE` | `setup` 或 `env`。                                                                 |
| `CPAMP_CPA_URL`             | `env` 模式下的 CPA URL。                                                           |
| `CPAMP_CPA_MANAGEMENT_KEY`  | `env` 模式下的 CPA Management Key。                                                |
| `CPAMP_OPERATION`           | `install`、`upgrade`、`repair` 或 `regenerate`。已有部署的非交互操作必须明确设置。 |
| `CPAMP_PROJECT_NAME`        | Docker Compose 项目名，默认 `cpamp`；需要在同一主机创建隔离的新部署时使用。        |

## 重跑和覆盖

以下 `CPAMP_OPERATION` 操作模式用于 Docker 部署；原生包继续使用原有的版本和覆盖参数。

脚本会在写文件前检查安装目录和 Docker 数据卷。检测到已有部署时，交互模式会提供：

1. **升级现有部署**：只执行镜像拉取和容器更新，不修改配置或密钥。
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

:::

## 启动和登录验证

Docker 安装、升级或修复后，脚本会等待 CPAMP 健康检查通过，再使用当前管理员密钥请求受保护的 Manager Server 接口。只有健康检查和管理员密钥验证都成功后，才会输出“安装步骤已完成”。

如果容器已启动但密钥验证失败，交互模式会询问是否自动停止 CPAMP 并修复管理员凭证；非交互模式会返回失败状态，并提示使用 `CPAMP_OPERATION=repair`。这可以避免用户拿到一个与旧数据库不匹配的“新密钥”。
