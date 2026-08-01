# 反向代理

如果你想用同一个域名访问 CPAMP 面板和 CPA API，就需要明确分流规则。HTTP 面板和 HTTP API 可以走反向代理；RESP Pub/Sub / RESP pop 不能走普通 HTTP 反向代理。

本文按同域名部署来说明：

- CPA Manager Plus 面板：`https://your-domain.com/management.html`
- CPA / CLI Proxy API：`https://your-domain.com/v1/...`
- CPA OAuth 回调、Codex API、Amp 路由等 CPA 侧接口

本文适用于 Full Docker / Manager Server 模式。CPAMP 轻量面板由 CPA 自己托管 `/management.html`，通常不需要这套混合分流。

## 先选场景

| 目标                                  | 推荐方式                                             |
| ------------------------------------- | ---------------------------------------------------- |
| 只代理 CPAMP 轻量面板                 | 所有管理页面和接口都转发到 CPA，不使用本页的混合分流 |
| CPAMP 和 CPA 使用不同域名             | 最简单：CPAMP 转发到 `18317`，CPA 转发到 `8317`      |
| CPAMP 面板和 CPA API 必须使用同一域名 | 继续使用本页的路径分流配置                           |

大多数用户建议为 CPAMP 和 CPA 使用不同域名或子域名。只有必须共用域名时，才需要理解下面的完整路径表。

## 路径边界

| 流量                                         | 推荐后端         | 说明                                                                                                    |
| -------------------------------------------- | ---------------- | ------------------------------------------------------------------------------------------------------- |
| `/management.html`                           | CPAMP `:18317`   | Manager Server 托管的管理面板。                                                                         |
| `/usage-service/*`                           | CPAMP `:18317`   | Manager Server 模式探测和配置接口。                                                                     |
| `/v0/management/*`                           | CPAMP `:18317`   | CPAMP 先处理用量、模型价格、别名、仪表盘、请求监控、Codex 账号巡检；其他管理接口再由 CPAMP 代理到 CPA。 |
| `/v0/resource/plugins/*`                     | CPAMP `:18317`   | CPAMP 面板中的插件页面资源；CPAMP 会按需代理到 CPA。                                                    |
| `/models`                                    | CPAMP `:18317`   | setup 后由 CPAMP 兼容代理到 CPA。                                                                       |
| `/v1/*`、`/v1beta/*`、`/backend-api/codex/*` | CPA `:8317`      | 实际模型 API、Codex API 和提供商请求。                                                                  |
| OAuth 回调                                   | CPA `:8317`      | 例如 `/anthropic/callback`、`/codex/callback`。                                                         |
| 未明确归属的新路径                           | CPA `:8317`      | 避免阻断 CPA 未来新增接口。                                                                             |
| RESP Pub/Sub / RESP pop                      | 直连 CPA `:8317` | 不能经过 HTTP 反向代理。                                                                                |

推荐架构：

```text
Browser
  -> https://your-domain.com
      -> /management.html       -> CPA Manager Plus :18317
      -> /usage-service/*       -> CPA Manager Plus :18317
      -> /v0/management/*       -> CPA Manager Plus :18317
      -> /v0/resource/plugins/* -> CPA Manager Plus :18317
      -> /v1/*, /backend-api/*  -> CPA :8317
      -> fallback               -> CPA :8317
```

如果采集器使用 RESP，Manager Server 到 CPA 的地址仍应配置为 CPA 的直连地址，例如 `http://cli-proxy-api:8317` 或内网 IP。不要把 RESP 连接指向只会处理 HTTP 的公网代理入口。

## 前置要求

CPA 与 CPAMP 是两个服务。常见端口：

```text
CPA / CLI Proxy API: 8317
CPA Manager Plus:    18317
```

CPA 配置中至少需要开启：

```yaml
remote-management:
  secret-key: '你的 CPA Management Key'
  allow-remote: true
```

两个容器即使在同一个 Docker network 内，CPA 看到的访问来源也通常不是 `localhost`，所以 `allow-remote: true` 是必要的。

推荐 CPA 版本：

```text
v7.1.39+
```

HTTP 用量队列最低要求：

```text
v6.10.8+
```

## Docker Compose 示例

下面示例假设：

- CPA 服务名为 `cli-proxy-api`。
- CPAMP 服务名为 `cpa-manager-plus`。
- Nginx、CPA、CPAMP 在同一个 Docker network 中。
- 对外只暴露 Nginx 的 `80` / `443`。

```yaml
services:
  cli-proxy-api:
    image: your-cpa-image:latest
    container_name: cli-proxy-api
    restart: unless-stopped
    volumes:
      - ./cliproxyapi/config.yaml:/app/config.yaml
      - ./cliproxyapi/auths:/app/auths
      - ./cliproxyapi/logs:/app/logs
    expose:
      - '8317'

  cpa-manager-plus:
    image: seakee/cpa-manager-plus:latest
    container_name: cpa-manager-plus
    restart: unless-stopped
    volumes:
      - ./cpa-manager-plus-data:/data
    expose:
      - '18317'
    depends_on:
      - cli-proxy-api

  nginx:
    image: nginx:alpine
    container_name: cpa-nginx
    restart: unless-stopped
    ports:
      - '80:80'
      # 如果使用 HTTPS，可以再暴露 443。
      # - "443:443"
    volumes:
      - ./nginx/conf.d:/etc/nginx/conf.d:ro
      # 如果使用 HTTPS，挂载证书目录。
      # - ./nginx/ssl:/etc/nginx/ssl:ro
    depends_on:
      - cli-proxy-api
      - cpa-manager-plus
```

如果 CPA 已经在宿主机运行，也可以不把 CPA 写入 Compose，只需要把 Nginx upstream 改成宿主机可访问的地址。

## Nginx HTTP 示例

在 `nginx.conf` 的 `http {}` 块中加入：

```nginx
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}
```

站点配置：

```nginx
upstream cpa_api {
    server cli-proxy-api:8317;
}

upstream cpamp {
    server cpa-manager-plus:18317;
}

server {
    listen 80;
    server_name your-domain.com;

    client_max_body_size 64m;

    proxy_http_version 1.1;
    proxy_read_timeout 3600s;
    proxy_send_timeout 3600s;
    proxy_buffering off;

    proxy_set_header Host              $host;
    proxy_set_header X-Real-IP         $remote_addr;
    proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header Upgrade           $http_upgrade;
    proxy_set_header Connection        $connection_upgrade;

    # 默认进入 CPAMP 面板
    location = / {
        return 302 /management.html;
    }

    # ===== CPA Manager Plus =====

    location = /management.html { proxy_pass http://cpamp; }
    location = /health          { proxy_pass http://cpamp; }
    location = /status          { proxy_pass http://cpamp; }
    location = /setup           { proxy_pass http://cpamp; }

    # CPAMP 兼容接口与运行时接口
    location ^~ /usage-service/ { proxy_pass http://cpamp; }

    # /v0/management/* 先进入 CPAMP：
    # - 用量 / 模型价格 / API 密钥别名 / 仪表盘 / 请求监控 /
    #   Codex 账号巡检 / 导入导出 等由 CPAMP 自己处理
    # - 其他 CPA 管理接口由 CPAMP 使用服务端保存的 CPA Management Key 继续代理到 CPA
    #
    # 注意：不要只配置 location = /v0/management
    # 应使用带尾斜杠的前缀匹配 /v0/management/
    location ^~ /v0/management/ { proxy_pass http://cpamp; }

    # CPAMP 插件页面资源。缺少此规则时，插件页面可能打开空白或资源 404。
    location ^~ /v0/resource/plugins/ { proxy_pass http://cpamp; }

    # /models 由 CPAMP 提供兼容代理
    # 如果 CPAMP 尚未完成 setup，访问此路径可能返回 412
    location = /models          { proxy_pass http://cpamp; }

    # ===== CPA / CLI Proxy API =====

    # OpenAI / Claude Code / Codex 等实际 API 请求应直接走 CPA
    location ^~ /v1/                 { proxy_pass http://cpa_api; }
    location ^~ /v1beta/             { proxy_pass http://cpa_api; }
    location ^~ /backend-api/codex/  { proxy_pass http://cpa_api; }
    location ^~ /api/                { proxy_pass http://cpa_api; }

    # CPA 特殊路由与 OAuth 回调
    location = /v1internal:method    { proxy_pass http://cpa_api; }
    location = /healthz              { proxy_pass http://cpa_api; }
    location = /anthropic/callback   { proxy_pass http://cpa_api; }
    location = /codex/callback       { proxy_pass http://cpa_api; }
    location = /google/callback      { proxy_pass http://cpa_api; }
    location = /antigravity/callback { proxy_pass http://cpa_api; }

    # 兜底给 CPA
    # 用于 CPA 根路径、Amp 路由以及未来新增接口
    location / {
        proxy_pass http://cpa_api;
    }
}
```

## HTTPS

HTTPS 只需要调整监听和证书，location 分流规则保持不变：

```nginx
server {
    listen 443 ssl http2;
    server_name your-domain.com;

    ssl_certificate     /etc/nginx/ssl/your-domain.com/fullchain.pem;
    ssl_certificate_key /etc/nginx/ssl/your-domain.com/privkey.pem;

    # 其余 proxy_set_header、location 配置同上。
}

server {
    listen 80;
    server_name your-domain.com;
    return 301 https://$host$request_uri;
}
```

生产环境建议由反向代理终止 TLS。Manager Server 默认监听 `0.0.0.0:18317`，可以通过 `HTTP_ADDR` 调整。

## Nginx 跑在宿主机

如果 Nginx 不在 Docker network 里，而是直接运行在宿主机上，可以把 upstream 改成宿主机端口：

```nginx
upstream cpa_api {
    server 127.0.0.1:8317;
}

upstream cpamp {
    server 127.0.0.1:18317;
}
```

前提是 CPA 与 CPAMP 都已经把端口映射到宿主机：

```yaml
ports:
  - '8317:8317'
```

```yaml
ports:
  - '18317:18317'
```

## CPAMP 首次 setup

访问：

```text
https://your-domain.com/management.html
```

首次 setup 填写：

```text
管理员密钥:         CPAMP 启动日志或 secret 文件中的 cpamp_...
CPA URL:            http://cli-proxy-api:8317
CPA Management Key: CPA remote-management.secret-key
```

注意：

- 登录 CPAMP 面板用的是 CPAMP 管理员密钥。
- CPA Management Key 是 CPAMP 服务端访问 CPA 时使用的，不再需要浏览器直接保存。
- CPA URL 推荐填写 Docker 内网地址：`http://cli-proxy-api:8317`。
- 不推荐在 CPA URL 里填写当前公网域名，否则会形成 `CPAMP -> Nginx -> CPAMP/CPA` 的回环代理链路，排障更复杂。

如果 CPAMP 是直接跑在宿主机上，CPA URL 可以填写：

```text
http://127.0.0.1:8317
```

如果 CPAMP 在 Docker 内、CPA 在宿主机上，Docker Desktop 可以尝试：

```text
http://host.docker.internal:8317
```

Linux Docker 环境则建议把 CPA 和 CPAMP 放到同一个 Docker network，使用服务名访问。

## 验证

配置完成后按顺序验证：

```bash
# 1. CPAMP 面板能打开
curl -I https://your-domain.com/management.html

# 2. CPAMP 健康检查
curl -i https://your-domain.com/health

# 3. CPA 健康检查
curl -i https://your-domain.com/healthz

# 4. CPAMP 运行时信息
curl -i https://your-domain.com/usage-service/info

# 5. CPA API 请求，应该命中 CPA
curl -i https://your-domain.com/v1/models \
  -H "Authorization: Bearer 你的 API 密钥"

# 6. CPAMP 代理的管理接口，应该先命中 CPAMP，再由 CPAMP 访问 CPA
curl -i https://your-domain.com/v0/management/config \
  -H "Authorization: Bearer 你的 CPAMP 管理员密钥"

# 7. 插件资源路径应该命中 CPAMP；不存在的资源可以返回 404，但不应被 Nginx 转到错误后端
curl -i https://your-domain.com/v0/resource/plugins/
```

## 排障

### `/management.html` 能打开，但 setup 保存 CPA 地址失败

优先检查 CPAMP 容器是否能访问 CPA：

```bash
docker exec -it cpa-manager-plus sh
wget -O- http://cli-proxy-api:8317/healthz
```

如果不通，检查：

- CPA 和 CPAMP 是否在同一个 Docker network。
- CPA 是否监听 `0.0.0.0:8317`。
- 服务名是否真的是 `cli-proxy-api`。
- CPA 容器是否正常启动。
- CPA 的 `remote-management.allow-remote` 是否为 `true`。

### 登录时应该填哪个密钥

CPAMP Full Docker / Manager Server 模式下：

```text
登录面板：     CPAMP 管理员密钥
连接 CPA：     CPA Management Key
普通 API 请求：CPA API 密钥
```

不要混用这三个密钥。

### `/models` 返回 412

通常说明 CPAMP 尚未完成首次 setup。先访问：

```text
https://your-domain.com/management.html
```

完成 setup 后再重试。

### 请求监控没有数据

按顺序检查：

1. CPA Management API 是否启用。
2. CPA Management Key 是否正确。
3. CPAMP 中是否开启请求监控。
4. CPA 是否启用了用量发布。
5. CPA 版本是否支持 HTTP 用量队列。
6. 是否有多个 CPAMP / Usage Service 同时消费同一个 CPA 用量队列。
7. CPAMP 是否持续运行，避免队列数据过期。

更多检查见 [请求监控排障](../troubleshooting/request-monitoring.md)。

### `/v1/models` 返回 401

`/v1/models` 是 CPA API 路由，不是 CPAMP 管理接口。这里应该使用普通 API 密钥：

```bash
curl -i https://your-domain.com/v1/models \
  -H "Authorization: Bearer 你的 API 密钥"
```

不要使用 CPAMP 管理员密钥或 CPA Management Key。

### `/v0/management/config` 返回 401

这个路径在本文方案中会先进入 CPAMP。Full Docker / Manager Server 模式下，浏览器侧登录使用的是 CPAMP 管理员密钥。如果手动 curl 管理接口，也应优先使用 CPAMP 管理员密钥：

```bash
curl -i https://your-domain.com/v0/management/config \
  -H "Authorization: Bearer 你的 CPAMP 管理员密钥"
```

### 部分 CPA 新接口不可用

确认最后有兜底规则：

```nginx
location / {
    proxy_pass http://cpa_api;
}
```

这样 CPA 新增的普通接口不会因为 Nginx 未显式配置 location 而被拦截。

### 插件页面空白或资源 404

确认插件资源路径进入 CPAMP：

```nginx
location ^~ /v0/resource/plugins/ { proxy_pass http://cpamp; }
```

这个路径由 CPAMP 面板加载插件页面资源时使用。不要把它交给普通 CPA API 兜底规则，否则插件页面可能能显示菜单但内容加载失败。

### 是否需要把 `/config`、`/logs`、`/auth-files` 也转给 CPAMP

一般不需要先手动加。推荐先只配置：

```nginx
location ^~ /v0/management/ { proxy_pass http://cpamp; }
location ^~ /v0/resource/plugins/ { proxy_pass http://cpamp; }
location ^~ /usage-service/ { proxy_pass http://cpamp; }
```

如果实际使用中发现 CPAMP 面板中的配置、日志、认证文件等页面出现 401 / 404，再根据具体路径补充精确规则。

不建议一开始就把所有不确定路径都转给 CPAMP，否则可能影响 CPA 原生接口或未来新增路由。
