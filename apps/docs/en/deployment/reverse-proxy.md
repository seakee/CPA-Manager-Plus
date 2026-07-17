# Reverse Proxy

Use a reverse proxy when one domain needs to serve both the CPAMP panel and CPA APIs. HTTP panels and HTTP APIs can be proxied; RESP Pub/Sub and RESP pop cannot go through a normal HTTP reverse proxy.

This guide uses a same-domain setup:

- CPA Manager Plus panel: `https://your-domain.com/management.html`
- CPA / CLI Proxy API: `https://your-domain.com/v1/...`
- CPA OAuth callbacks, Codex API, Amp routes, and other CPA-side endpoints

This applies to Full Docker / Manager Server mode. CPA itself serves `/management.html` for the CPAMP Lightweight Panel, so this mixed routing is usually unnecessary.

## Choose Your Scenario

| Goal                                        | Recommended approach                                                |
| ------------------------------------------- | ------------------------------------------------------------------- |
| Proxy only the CPAMP Lightweight Panel      | Send all management pages and APIs to CPA; do not use mixed routing |
| Use separate domains for CPAMP and CPA      | Simplest: send CPAMP to `18317` and CPA to `8317`                   |
| Use one domain for CPAMP panel and CPA APIs | Continue with the path-routing configuration on this page           |

For most deployments, use separate domains or subdomains for CPAMP and CPA. Read the full route table only when both must share one domain.

## Route Boundaries

| Traffic                                      | Recommended backend | Notes                                                                                                                                                  |
| -------------------------------------------- | ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `/management.html`                           | CPAMP `:18317`      | Manager Server-hosted management panel.                                                                                                                |
| `/usage-service/*`                           | CPAMP `:18317`      | Manager Server mode detection and config APIs.                                                                                                         |
| `/v0/management/*`                           | CPAMP `:18317`      | CPAMP handles usage, model-prices, aliases, dashboard, monitoring, and codex-inspection first; other management APIs are then proxied by CPAMP to CPA. |
| `/v0/resource/plugins/*`                     | CPAMP `:18317`      | Plugin page resources used by the CPAMP panel. CPAMP proxies them to CPA when needed.                                                                  |
| `/models`                                    | CPAMP `:18317`      | Compatibility proxy to CPA after setup.                                                                                                                |
| `/v1/*`, `/v1beta/*`, `/backend-api/codex/*` | CPA `:8317`         | Actual model API, Codex API, and provider requests.                                                                                                    |
| OAuth callback                               | CPA `:8317`         | For example `/anthropic/callback` and `/codex/callback`.                                                                                               |
| New paths without explicit ownership         | CPA `:8317`         | Avoid blocking future CPA endpoints.                                                                                                                   |
| RESP Pub/Sub / RESP pop                      | Direct CPA `:8317`  | Cannot go through an HTTP reverse proxy.                                                                                                               |

Recommended architecture:

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

If the collector uses RESP, the Manager Server to CPA address should still be a direct CPA address, such as `http://cli-proxy-api:8317` or an internal IP. Do not point RESP connections at a public proxy entry that only handles HTTP.

## Prerequisites

CPA and CPAMP are two services. Common ports:

```text
CPA / CLI Proxy API: 8317
CPA Manager Plus:    18317
```

CPA must have Management API enabled:

```yaml
remote-management:
  secret-key: 'your CPA Management Key'
  allow-remote: true
```

Even when both containers are in the same Docker network, CPA usually does not see the request source as `localhost`, so `allow-remote: true` is required.

Recommended CPA version:

```text
v7.1.39+
```

Minimum for HTTP usage queue:

```text
v6.10.8+
```

## Docker Compose Example

The example assumes:

- CPA service name: `cli-proxy-api`.
- CPAMP service name: `cpa-manager-plus`.
- Nginx, CPA, and CPAMP are in the same Docker network.
- Only Nginx exposes `80` / `443` publicly.

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
      # Expose 443 if HTTPS is enabled.
      # - "443:443"
    volumes:
      - ./nginx/conf.d:/etc/nginx/conf.d:ro
      # Mount certificates if HTTPS is enabled.
      # - ./nginx/ssl:/etc/nginx/ssl:ro
    depends_on:
      - cli-proxy-api
      - cpa-manager-plus
```

If CPA already runs on the host machine, omit the CPA service and point the Nginx upstream to a host-accessible address instead.

## Nginx HTTP Example

Add this to the `http {}` block in `nginx.conf`:

```nginx
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}
```

Site config:

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

    # Default entry point: CPAMP panel
    location = / {
        return 302 /management.html;
    }

    # ===== CPA Manager Plus =====

    location = /management.html { proxy_pass http://cpamp; }
    location = /health          { proxy_pass http://cpamp; }
    location = /status          { proxy_pass http://cpamp; }
    location = /setup           { proxy_pass http://cpamp; }

    # CPAMP compatibility and runtime APIs
    location ^~ /usage-service/ { proxy_pass http://cpamp; }

    # /v0/management/* goes to CPAMP first:
    # - usage / model-prices / api-key-aliases / dashboard / monitoring /
    #   codex-inspection / import-export are handled by CPAMP
    # - other CPA management APIs are proxied by CPAMP to CPA using the saved CPA Management Key
    #
    # Do not only configure location = /v0/management
    # Use the prefix match with trailing slash: /v0/management/
    location ^~ /v0/management/ { proxy_pass http://cpamp; }

    # CPAMP plugin page resources. Without this rule, plugin pages may be blank or return 404 for assets.
    location ^~ /v0/resource/plugins/ { proxy_pass http://cpamp; }

    # /models is provided through CPAMP's compatibility proxy
    # If CPAMP has not completed setup yet, this path may return 412
    location = /models          { proxy_pass http://cpamp; }

    # ===== CPA / CLI Proxy API =====

    # Actual OpenAI / Claude Code / Codex API requests should go directly to CPA
    location ^~ /v1/                 { proxy_pass http://cpa_api; }
    location ^~ /v1beta/             { proxy_pass http://cpa_api; }
    location ^~ /backend-api/codex/  { proxy_pass http://cpa_api; }
    location ^~ /api/                { proxy_pass http://cpa_api; }

    # CPA special routes and OAuth callbacks
    location = /v1internal:method    { proxy_pass http://cpa_api; }
    location = /healthz              { proxy_pass http://cpa_api; }
    location = /anthropic/callback   { proxy_pass http://cpa_api; }
    location = /codex/callback       { proxy_pass http://cpa_api; }
    location = /google/callback      { proxy_pass http://cpa_api; }
    location = /antigravity/callback { proxy_pass http://cpa_api; }

    # Fallback to CPA
    # This covers CPA root routes, Amp routes, and future CPA endpoints
    location / {
        proxy_pass http://cpa_api;
    }
}
```

## HTTPS

For HTTPS, only the listener and certificate settings change. Keep the same location routing rules:

```nginx
server {
    listen 443 ssl http2;
    server_name your-domain.com;

    ssl_certificate     /etc/nginx/ssl/your-domain.com/fullchain.pem;
    ssl_certificate_key /etc/nginx/ssl/your-domain.com/privkey.pem;

    # Keep the same proxy_set_header and location blocks as above.
}

server {
    listen 80;
    server_name your-domain.com;
    return 301 https://$host$request_uri;
}
```

In production, terminate TLS at the reverse proxy. Manager Server listens on `0.0.0.0:18317` by default. Use `HTTP_ADDR` to change it.

## Nginx Runs On The Host

If Nginx runs directly on the host instead of inside the Docker network, change the upstreams to host ports:

```nginx
upstream cpa_api {
    server 127.0.0.1:8317;
}

upstream cpamp {
    server 127.0.0.1:18317;
}
```

Make sure both services expose ports to the host:

```yaml
ports:
  - '8317:8317'
```

```yaml
ports:
  - '18317:18317'
```

## CPAMP First Setup

Open:

```text
https://your-domain.com/management.html
```

During first setup, enter:

```text
Admin Key:          cpamp_... from CPAMP startup logs or the secret file
CPA URL:            http://cli-proxy-api:8317
CPA Management Key: CPA remote-management.secret-key
```

Important notes:

- CPAMP panel login uses the CPAMP Admin Key.
- The CPA Management Key is stored server-side and used by CPAMP when talking to CPA.
- The recommended CPA URL is the Docker internal URL: `http://cli-proxy-api:8317`.
- Avoid using the public domain itself as the CPA URL, because it creates a loop like `CPAMP -> Nginx -> CPAMP/CPA`, which makes troubleshooting harder.

If CPAMP runs directly on the host, the CPA URL can be:

```text
http://127.0.0.1:8317
```

If CPAMP runs in Docker and CPA runs on the host, Docker Desktop may support:

```text
http://host.docker.internal:8317
```

For Linux Docker environments, it is usually better to put CPA and CPAMP in the same Docker network and use the service name.

## Verification

After configuring Nginx, test in this order:

```bash
# 1. CPAMP panel should be reachable
curl -I https://your-domain.com/management.html

# 2. CPAMP health check
curl -i https://your-domain.com/health

# 3. CPA health check
curl -i https://your-domain.com/healthz

# 4. CPAMP runtime info
curl -i https://your-domain.com/usage-service/info

# 5. CPA API request, should hit CPA
curl -i https://your-domain.com/v1/models \
  -H "Authorization: Bearer your API Key"

# 6. CPAMP-proxied management API, should hit CPAMP first and then CPA
curl -i https://your-domain.com/v0/management/config \
  -H "Authorization: Bearer your CPAMP Admin Key"

# 7. Plugin resource path should hit CPAMP. A missing resource may return 404,
# but it should not be sent to the wrong upstream by Nginx.
curl -i https://your-domain.com/v0/resource/plugins/
```

## Troubleshooting

### `/management.html` opens, but setup cannot save the CPA URL

Check whether the CPAMP container can reach CPA:

```bash
docker exec -it cpa-manager-plus sh
wget -O- http://cli-proxy-api:8317/healthz
```

If it fails, check:

- Whether CPA and CPAMP are in the same Docker network.
- Whether CPA listens on `0.0.0.0:8317`.
- Whether the service name is really `cli-proxy-api`.
- Whether the CPA container is running.
- Whether `remote-management.allow-remote` is set to `true`.

### Which key should I use?

In CPAMP Full Docker / Manager Server mode:

```text
Panel login:        CPAMP Admin Key
CPA connection:     CPA Management Key
Normal API request: CPA API Key
```

Do not mix these three keys.

### `/models` returns 412

This usually means CPAMP has not completed first setup yet. Open:

```text
https://your-domain.com/management.html
```

Complete setup first, then retry.

### Request monitoring has no data

Check these items in order:

1. CPA Management API is enabled.
2. CPA Management Key is correct.
3. Request monitoring is enabled in CPAMP.
4. CPA usage publishing is enabled.
5. CPA version supports HTTP usage queue.
6. No other CPAMP / Usage Service instance is consuming the same CPA usage queue.
7. CPAMP keeps running continuously so queue items do not expire before collection.

For more checks, see [Request Monitoring Troubleshooting](../troubleshooting/request-monitoring.md).

### `/v1/models` returns 401

`/v1/models` is a CPA API route, not a CPAMP management route. Use a normal API Key:

```bash
curl -i https://your-domain.com/v1/models \
  -H "Authorization: Bearer your API Key"
```

Do not use the CPAMP Admin Key or CPA Management Key.

### `/v0/management/config` returns 401

In this setup, this path goes to CPAMP first. In Full Docker / Manager Server mode, browser-side login uses the CPAMP Admin Key. If you manually call management APIs with curl, use the CPAMP Admin Key first:

```bash
curl -i https://your-domain.com/v0/management/config \
  -H "Authorization: Bearer your CPAMP Admin Key"
```

### Some new CPA endpoints do not work

Make sure the fallback rule exists:

```nginx
location / {
    proxy_pass http://cpa_api;
}
```

This ensures future CPA endpoints are not blocked just because they do not have explicit Nginx location rules yet.

### Plugin pages are blank or plugin resources return 404

Make sure plugin resources go to CPAMP:

```nginx
location ^~ /v0/resource/plugins/ { proxy_pass http://cpamp; }
```

The CPAMP panel uses this path to load plugin page resources. Do not send it to the normal CPA fallback, or a plugin menu may appear while its page content fails to load.

### Should `/config`, `/logs`, and `/auth-files` be proxied to CPAMP?

Usually, do not add them at first. Start with:

```nginx
location ^~ /v0/management/ { proxy_pass http://cpamp; }
location ^~ /v0/resource/plugins/ { proxy_pass http://cpamp; }
location ^~ /usage-service/ { proxy_pass http://cpamp; }
```

If the CPAMP panel later shows 401 / 404 on config, logs, or auth file pages, add precise routing rules based on the actual failing path.

Avoid sending every uncertain path to CPAMP by default, because it may interfere with native CPA routes or future CPA endpoints.
