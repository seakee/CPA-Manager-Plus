FROM --platform=$BUILDPLATFORM alpine:3.21 AS cpa-download

ARG VERSION
ARG SOURCE_COMMIT
# Fail closed on malformed identity before any package or network operation.
# The format is the same one bin/release/image-evidence.py enforces.
RUN set -eu; \
    test -n "$VERSION"; \
    test "${#VERSION}" -le 128; \
    case "$VERSION" in [!A-Za-z0-9]* | *[!A-Za-z0-9._+-]*) exit 1 ;; esac; \
    case "$(printf '%s' "$VERSION" | tr 'A-Z' 'a-z')" in unknown) exit 1 ;; esac; \
    test "${#SOURCE_COMMIT}" -eq 40; \
    case "$SOURCE_COMMIT" in *[!0-9a-f]*) exit 1 ;; esac
ARG TARGETOS
ARG TARGETARCH
RUN set -eu; \
    test "$TARGETOS" = "linux"; \
    cpa_version="7.3.3"; \
    case "$TARGETARCH" in \
      amd64) asset_arch="amd64"; asset_sha256="7af8c99cd08eee3ccc81d1596e8a31785674d3de6bd7ec61416d59493dd8fc01" ;; \
      arm64) asset_arch="aarch64"; asset_sha256="5f320e3fae52af00f07b78201311e9d096b36e759441d948de48a10f49e71883" ;; \
      *) echo "unsupported CPA target architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    asset_name="CLIProxyAPI_${cpa_version}_linux_${asset_arch}.tar.gz"; \
    apk add --no-cache ca-certificates wget; \
    wget -qO "/tmp/${asset_name}" "https://github.com/router-for-me/CLIProxyAPI/releases/download/v${cpa_version}/${asset_name}"; \
    printf '%s  %s\n' "$asset_sha256" "/tmp/${asset_name}" | sha256sum -c -; \
    mkdir -p /out; \
    tar -xOzf "/tmp/${asset_name}" cli-proxy-api > /out/cli-proxy-api; \
    tar -xOzf "/tmp/${asset_name}" LICENSE > /out/CPA-LICENSE; \
    test -s /out/CPA-LICENSE; \
    chmod 0444 /out/CPA-LICENSE; \
    chmod 0755 /out/cli-proxy-api; \
    binary_sha256="$(sha256sum /out/cli-proxy-api | awk '{print $1}')"; \
    printf '{"schemaVersion":1,"engine":"cpa","version":"%s","artifactId":"sha256:%s"}\n' \
      "$cpa_version" "$binary_sha256" > /out/cpa-artifact.json; \
    chmod 0444 /out/cpa-artifact.json

# Official v8 pin shared by the default bundle and private amd64 proofs.
# No version/hash override is accepted for this pin.
FROM --platform=$BUILDPLATFORM alpine:3.21 AS cpa-v8-download

ARG VERSION
ARG SOURCE_COMMIT
# Fail closed on malformed identity before any package or network operation.
# The format is the same one bin/release/image-evidence.py enforces.
RUN set -eu; \
    test -n "$VERSION"; \
    test "${#VERSION}" -le 128; \
    case "$VERSION" in [!A-Za-z0-9]* | *[!A-Za-z0-9._+-]*) exit 1 ;; esac; \
    case "$(printf '%s' "$VERSION" | tr 'A-Z' 'a-z')" in unknown) exit 1 ;; esac; \
    test "${#SOURCE_COMMIT}" -eq 40; \
    case "$SOURCE_COMMIT" in *[!0-9a-f]*) exit 1 ;; esac
ARG TARGETOS
ARG TARGETARCH
RUN set -eu; \
    test "$TARGETOS" = "linux"; \
    case "$TARGETARCH" in \
      amd64) asset_arch="amd64"; \
        archive_sha256="4cd593251db85e50c820f42e6ea5cecdd05f902201d3dbdb025671d4f306a123"; \
        executable_sha256="e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7" ;; \
      arm64) asset_arch="aarch64"; \
        archive_sha256="62c136d7c6ee03cedebaad4be3e7fb9bcf14d095c96597fd92e68554c2a785c5"; \
        executable_sha256="4a5b43cc3700d36c72b7c1953e9b11eca0888c13b789ea2f99471117504a7d5d" ;; \
      *) echo "unsupported official CPA v8 target: $TARGETOS/$TARGETARCH (no v7 fallback)" >&2; exit 1 ;; \
    esac; \
    asset_name="CLIProxyAPI_8.0.55_linux_${asset_arch}.tar.gz"; \
    apk add --no-cache ca-certificates wget; \
    wget -qO "/tmp/${asset_name}" "https://github.com/router-for-me/CLIProxyAPI/releases/download/v8.0.55/${asset_name}"; \
    printf '%s  %s\n' "$archive_sha256" "/tmp/${asset_name}" | sha256sum -c -; \
    mkdir -p /out; \
    tar -xOzf "/tmp/${asset_name}" cli-proxy-api > /out/cli-proxy-api; \
    printf '%s  %s\n' "$executable_sha256" /out/cli-proxy-api | sha256sum -c -; \
    tar -xOzf "/tmp/${asset_name}" LICENSE > /out/CPA-LICENSE; \
    test -s /out/CPA-LICENSE; \
    chmod 0755 /out/cli-proxy-api; \
    printf '{"schemaVersion":1,"engine":"cpa","version":"8.0.55","artifactId":"sha256:%s"}\n' \
      "$executable_sha256" > /out/cpa-artifact.json; \
    chmod 0444 /out/cpa-artifact.json /out/CPA-LICENSE

FROM --platform=$BUILDPLATFORM golang:1.26.10-alpine AS supervisor-build

ARG VERSION
ARG SOURCE_COMMIT
# Fail closed on malformed identity before the Go build and license collection.
# The format is the same one bin/release/image-evidence.py enforces.
RUN set -eu; \
    test -n "$VERSION"; \
    test "${#VERSION}" -le 128; \
    case "$VERSION" in [!A-Za-z0-9]* | *[!A-Za-z0-9._+-]*) exit 1 ;; esac; \
    case "$(printf '%s' "$VERSION" | tr 'A-Z' 'a-z')" in unknown) exit 1 ;; esac; \
    test "${#SOURCE_COMMIT}" -eq 40; \
    case "$SOURCE_COMMIT" in *[!0-9a-f]*) exit 1 ;; esac
RUN apk add --no-cache python3
COPY bin/release/collect-third-party-licenses.py /collect-third-party-licenses.py
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src/apps/runtime-supervisor
COPY apps/runtime-supervisor/go.mod apps/runtime-supervisor/go.sum ./
RUN go mod download
RUN python3 /collect-third-party-licenses.py go --module /src/apps/runtime-supervisor --component runtime-supervisor --output /out/licenses/runtime-supervisor
COPY apps/runtime-supervisor ./
RUN test "$TARGETOS" = "linux"
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/cpamp-runtime-supervisor ./cmd/cpamp-runtime-supervisor
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/cpamp-launcher ./cmd/cpamp-launcher

FROM --platform=$BUILDPLATFORM node:22.23.4-alpine AS web-build
ARG VERSION
ARG SOURCE_COMMIT
# Fail closed on malformed identity before any Web or Go build runs.
# The format is the same one bin/release/image-evidence.py enforces.
RUN set -eu; \
    test -n "$VERSION"; \
    test "${#VERSION}" -le 128; \
    case "$VERSION" in [!A-Za-z0-9]* | *[!A-Za-z0-9._+-]*) exit 1 ;; esac; \
    case "$(printf '%s' "$VERSION" | tr 'A-Z' 'a-z')" in unknown) exit 1 ;; esac; \
    test "${#SOURCE_COMMIT}" -eq 40; \
    case "$SOURCE_COMMIT" in *[!0-9a-f]*) exit 1 ;; esac
WORKDIR /app
COPY package*.json ./
COPY apps/web/package.json ./apps/web/package.json
RUN npm ci
RUN apk add --no-cache python3
COPY bin/release/collect-third-party-licenses.py /collect-third-party-licenses.py
COPY bin/release/third-party-license-sources/ /third-party-license-sources/
RUN python3 /collect-third-party-licenses.py web --root /app --output /out/licenses/web
COPY apps/web ./apps/web
# Full Web TypeScript tests import this source-shared R1 fixture.
# Keep the in-image module path identical to the repository layout.
COPY tests/fixtures/r1-embedded-api-transport.json ./tests/fixtures/r1-embedded-api-transport.json
COPY bin/build ./bin/build
COPY bin/release/hash-fullweb.mjs ./bin/release/hash-fullweb.mjs
WORKDIR /app/apps/web
RUN VERSION="$VERSION" npm run build
RUN mkdir -p /out && node /app/bin/release/hash-fullweb.mjs /app/apps/web/dist/fullweb /out/fullweb-manifest.json

FROM --platform=$BUILDPLATFORM golang:1.26.10-alpine AS service-build
ARG VERSION
ARG SOURCE_COMMIT
# Fail closed on malformed identity before the Go build and license collection.
# The format is the same one bin/release/image-evidence.py enforces.
RUN set -eu; \
    test -n "$VERSION"; \
    test "${#VERSION}" -le 128; \
    case "$VERSION" in [!A-Za-z0-9]* | *[!A-Za-z0-9._+-]*) exit 1 ;; esac; \
    case "$(printf '%s' "$VERSION" | tr 'A-Z' 'a-z')" in unknown) exit 1 ;; esac; \
    test "${#SOURCE_COMMIT}" -eq 40; \
    case "$SOURCE_COMMIT" in *[!0-9a-f]*) exit 1 ;; esac
RUN apk add --no-cache python3
COPY bin/release/collect-third-party-licenses.py /collect-third-party-licenses.py
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY apps/manager-server ./apps/manager-server
COPY tests/fixtures/r1-embedded-api-transport.json ./apps/manager-server/internal/domain/routing/release-embedded-api-transport.json
COPY --from=web-build /app/apps/web/dist/panel/management.html ./apps/manager-server/internal/httpapi/web/management.html
COPY --from=web-build /app/apps/web/dist/fullweb/ ./apps/manager-server/internal/httpapi/web/fullweb/
COPY --from=web-build /out/fullweb-manifest.json ./apps/manager-server/internal/httpapi/release-fullweb-manifest.json
WORKDIR /src/apps/manager-server
RUN go mod download
RUN python3 /collect-third-party-licenses.py go --module /src/apps/manager-server --component manager --output /out/licenses/manager
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags "-s -w -X github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo.Version=$VERSION -X github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo.SourceCommit=$SOURCE_COMMIT" -o /out/cpa-manager-plus ./cmd/cpa-manager-plus
# Verify the real output binary carries this source stamp, not just image labels.
RUN go version -m /out/cpa-manager-plus | grep -F "SourceCommit=$SOURCE_COMMIT"

# Packaging smoke without activating production routing authority. The final
# image consumes the same compiled artifact only after this check passes.
FROM service-build AS web-package-test
RUN CPAMP_FULLWEB_PACKAGED_TEST=1 go test -run '^TestPackagedFullWeb(Dist|Manifest)$' ./internal/httpapi
RUN go test -run '^TestR1EmbeddedPublicAPITransportFixture$' ./internal/domain/routing

FROM --platform=$BUILDPLATFORM golang:1.26.10-alpine AS ingress-build
ARG VERSION
ARG SOURCE_COMMIT
ARG TARGETOS
ARG TARGETARCH
# Fail closed on malformed identity before the Go build and license collection.
# The format is the same one bin/release/image-evidence.py enforces.
RUN set -eu; \
    test -n "$VERSION"; \
    test "${#VERSION}" -le 128; \
    case "$VERSION" in [!A-Za-z0-9]* | *[!A-Za-z0-9._+-]*) exit 1 ;; esac; \
    case "$(printf '%s' "$VERSION" | tr 'A-Z' 'a-z')" in unknown) exit 1 ;; esac; \
    test "${#SOURCE_COMMIT}" -eq 40; \
    case "$SOURCE_COMMIT" in *[!0-9a-f]*) exit 1 ;; esac
RUN apk add --no-cache python3
COPY bin/release/collect-third-party-licenses.py /collect-third-party-licenses.py
WORKDIR /src/apps/ingress
COPY apps/ingress ./
RUN go mod download
RUN python3 /collect-third-party-licenses.py go --module /src/apps/ingress --component ingress --output /out/licenses/ingress
RUN test "$TARGETOS" = "linux"
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/cpamp-ingress ./cmd/cpamp-ingress

# Extract the minimal glibc runtime required by the official CGO-built CPA
# binary and snapshot the owning Debian package metadata and copyright texts.
FROM debian:bookworm-slim AS glibc-runtime
ARG VERSION
ARG SOURCE_COMMIT
ARG TARGETOS
ARG TARGETARCH
RUN set -eu; \
    test -n "$VERSION"; \
    test "${#VERSION}" -le 128; \
    case "$VERSION" in [!A-Za-z0-9]* | *[!A-Za-z0-9._+-]*) exit 1 ;; esac; \
    case "$(printf '%s' "$VERSION" | tr 'A-Z' 'a-z')" in unknown) exit 1 ;; esac; \
    test "${#SOURCE_COMMIT}" -eq 40; \
    case "$SOURCE_COMMIT" in *[!0-9a-f]*) exit 1 ;; esac
COPY --from=cpa-v8-download /out/cli-proxy-api /tmp/cli-proxy-api
RUN set -eu; \
    test "$TARGETOS" = "linux"; \
    case "$TARGETARCH" in \
      amd64) gnu_triple="x86_64-linux-gnu"; loader_dir="/lib64"; loader_name="ld-linux-x86-64.so.2" ;; \
      arm64) gnu_triple="aarch64-linux-gnu"; loader_dir="/lib"; loader_name="ld-linux-aarch64.so.1" ;; \
      *) echo "unsupported target architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    ldd_out="$(ldd /tmp/cli-proxy-api)"; \
    printf '%s\n' "$ldd_out"; \
    for soname in libc.so.6 libdl.so.2 libresolv.so.2 libpthread.so.0 "$loader_name"; do \
      printf '%s\n' "$ldd_out" | grep -F "$soname" >/dev/null; \
    done; \
    unexpected="$(printf '%s\n' "$ldd_out" | awk '{print $1}' | grep -vE '^(linux-vdso\.so\.1|libc\.so\.6|libdl\.so\.2|libresolv\.so\.2|libpthread\.so\.0|/lib64/ld-linux-x86-64\.so\.2|/lib/ld-linux-aarch64\.so\.1)$' || true)"; \
    test -z "$unexpected"; \
    mkdir -p "/glibc-rootfs/lib/${gnu_triple}" "/glibc-rootfs${loader_dir}"; \
    for lib in libc.so.6 libdl.so.2 libresolv.so.2 libpthread.so.0 libgcc_s.so.1 "$loader_name"; do \
      cp -L "/lib/${gnu_triple}/${lib}" "/glibc-rootfs/lib/${gnu_triple}/${lib}"; \
    done; \
    ln -sf "/lib/${gnu_triple}/${loader_name}" "/glibc-rootfs${loader_dir}/${loader_name}"; \
    rm -f /tmp/cli-proxy-api; \
    mkdir -p /glibc-base/usr/share/doc; \
    dpkg-query -W -f='${binary:Package}\t${Version}\t${source:Package}\n' libc6 libgcc-s1 gcc-12-base > /glibc-base/base-packages.tsv; \
    cp -a /usr/share/doc/libc6 /usr/share/doc/libgcc-s1 /usr/share/doc/gcc-12-base /glibc-base/usr/share/doc/

FROM alpine:3.21 AS cpamp-base
ARG VERSION
ARG SOURCE_COMMIT
# Fail closed on malformed identity before any package or network operation.
# The format is the same one bin/release/image-evidence.py enforces.
RUN set -eu; \
    test -n "$VERSION"; \
    test "${#VERSION}" -le 128; \
    case "$VERSION" in [!A-Za-z0-9]* | *[!A-Za-z0-9._+-]*) exit 1 ;; esac; \
    case "$(printf '%s' "$VERSION" | tr 'A-Z' 'a-z')" in unknown) exit 1 ;; esac; \
    test "${#SOURCE_COMMIT}" -eq 40; \
    case "$SOURCE_COMMIT" in *[!0-9a-f]*) exit 1 ;; esac
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 10001 -S cpamp-cpa \
    && adduser -u 10001 -S -G cpamp-cpa -H -h /nonexistent -s /sbin/nologin cpamp-cpa \
    && addgroup -g 10002 -S cpamp-manager \
    && adduser -u 10002 -S -G cpamp-manager -H -h /nonexistent -s /sbin/nologin cpamp-manager \
    && addgroup -g 10003 -S cpamp-ingress \
    && adduser -u 10003 -S -G cpamp-ingress -H -h /nonexistent -s /sbin/nologin cpamp-ingress
COPY --from=glibc-runtime /glibc-rootfs/ /

# Snapshot the final package database before adding the audit-only interpreter.
FROM cpamp-base AS slim-evidence
ARG VERSION
ARG SOURCE_COMMIT
ARG TARGETOS
ARG TARGETARCH
# Fail closed on malformed identity before the snapshot and audit install.
# The format is the same one bin/release/image-evidence.py enforces.
RUN set -eu; \
    test -n "$VERSION"; \
    test "${#VERSION}" -le 128; \
    case "$VERSION" in [!A-Za-z0-9]* | *[!A-Za-z0-9._+-]*) exit 1 ;; esac; \
    case "$(printf '%s' "$VERSION" | tr 'A-Z' 'a-z')" in unknown) exit 1 ;; esac; \
    test "${#SOURCE_COMMIT}" -eq 40; \
    case "$SOURCE_COMMIT" in *[!0-9a-f]*) exit 1 ;; esac
RUN mkdir -p /base/lib/apk/db && cp /lib/apk/db/installed /base/lib/apk/db/installed \
    && if test -d /usr/share/licenses; then mkdir -p /base/usr/share; cp -a /usr/share/licenses /base/usr/share/; fi
COPY --from=glibc-runtime /glibc-base /glibc-base
RUN apk add --no-cache python3
COPY bin/release/collect-third-party-licenses.py bin/release/image-evidence.py /tools/
COPY bin/release/embedded-third-party-inventory.txt /source-inventory.json
COPY LICENSE /image/usr/local/share/licenses/cpamp/LICENSE
COPY --from=supervisor-build /out/cpamp-launcher /image/usr/local/bin/cpamp-launcher
COPY --from=supervisor-build /out/cpamp-runtime-supervisor /image/usr/local/bin/cpamp-runtime-supervisor
COPY --from=web-package-test /out/cpa-manager-plus /image/usr/local/bin/cpa-manager-plus
COPY --from=ingress-build /out/cpamp-ingress /image/usr/local/bin/cpamp-ingress
COPY --from=service-build /out/licenses/manager/ /image/usr/local/share/licenses/manager/
COPY --from=web-build /out/licenses/web/ /image/usr/local/share/licenses/web/
COPY --from=ingress-build /out/licenses/ingress/ /image/usr/local/share/licenses/ingress/
COPY --from=supervisor-build /out/licenses/runtime-supervisor/ /image/usr/local/share/licenses/runtime-supervisor/
COPY --from=web-build /out/fullweb-manifest.json /image/usr/local/share/cpamp/release/fullweb-manifest.json
COPY --from=cpa-v8-download /out/cpa-artifact.json /image/usr/local/share/cpamp/runtime/cpa-artifact.json
RUN python3 /tools/collect-third-party-licenses.py base --kind alpine --root /base --output /image/usr/local/share/licenses/base-image \
    && python3 /tools/collect-third-party-licenses.py base --kind debian --component glibc-runtime --root /glibc-base --output /image/usr/local/share/licenses/glibc-runtime \
    && python3 /tools/image-evidence.py --root /image --role cpamp --without-cpa --version "$VERSION" --source-commit "$SOURCE_COMMIT" \
      --platform "$TARGETOS/$TARGETARCH" --source-inventory /source-inventory.json

FROM slim-evidence AS full-evidence
ARG VERSION
ARG SOURCE_COMMIT
ARG TARGETOS
ARG TARGETARCH
COPY --from=cpa-v8-download /out/cli-proxy-api /image/usr/local/bin/cli-proxy-api
COPY --from=cpa-v8-download /out/cpa-artifact.json /image/usr/local/share/cpamp/runtime/cpa-artifact.json
COPY --from=cpa-v8-download /out/CPA-LICENSE /image/usr/local/share/licenses/cli-proxy-api/LICENSE
RUN python3 /tools/image-evidence.py --root /image --role cpamp --version "$VERSION" --source-commit "$SOURCE_COMMIT" \
      --platform "$TARGETOS/$TARGETARCH" --source-inventory /source-inventory.json

FROM slim-evidence AS v7-evidence
ARG VERSION
ARG SOURCE_COMMIT
ARG TARGETOS
ARG TARGETARCH
COPY --from=cpa-download /out/cli-proxy-api /image/usr/local/bin/cli-proxy-api
COPY --from=cpa-download /out/cpa-artifact.json /image/usr/local/share/cpamp/runtime/cpa-artifact.json
COPY --from=cpa-download /out/CPA-LICENSE /image/usr/local/share/licenses/cli-proxy-api/LICENSE
RUN python3 /tools/image-evidence.py --root /image --role cpamp --version "$VERSION" --source-commit "$SOURCE_COMMIT" \
      --platform "$TARGETOS/$TARGETARCH" --source-inventory /source-inventory.json

FROM cpamp-base AS slim
ARG VERSION
ARG SOURCE_COMMIT
LABEL org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$SOURCE_COMMIT \
      org.opencontainers.image.source="https://github.com/seakee/CPA-Manager-Plus"
COPY --from=supervisor-build /out/cpamp-launcher /usr/local/bin/cpamp-launcher
COPY --from=supervisor-build /out/cpamp-runtime-supervisor /usr/local/bin/cpamp-runtime-supervisor
COPY --from=web-package-test /out/cpa-manager-plus /usr/local/bin/cpa-manager-plus
COPY --from=ingress-build /out/cpamp-ingress /usr/local/bin/cpamp-ingress
COPY tests/fixtures/r1-embedded-api-transport.json /usr/local/share/cpamp/release/public-api-transport-contract.json
COPY --from=slim-evidence /image/usr/local/share/licenses/ /usr/local/share/licenses/
COPY --from=slim-evidence /image/usr/local/share/cpamp/ /usr/local/share/cpamp/
COPY docker/runtime/config.v8.default.seed.yaml /usr/local/share/cpamp/runtime/config.seed.yaml
ENV USAGE_DATA_DIR=/data \
    CPA_UPSTREAM_URL=http://127.0.0.1:8317 \
    USAGE_COLLECTOR_MODE=auto \
    USAGE_RESP_QUEUE=usage \
    USAGE_RESP_POP_SIDE=right \
    USAGE_BATCH_SIZE=100 \
    USAGE_POLL_INTERVAL_MS=500 \
    USAGE_QUERY_LIMIT=50000 \
    USAGE_CORS_ORIGINS=*
EXPOSE 18317
HEALTHCHECK --interval=5s --timeout=3s --retries=3 CMD ["sh", "-c", "nc -z 127.0.0.1 18317 && nc -z 127.0.0.1 18319 && nc -z 127.0.0.1 9081"]
ENTRYPOINT ["/usr/local/bin/cpamp-launcher"]

# Historical lifecycle regressions only; never selected by normal Compose.
FROM slim AS v7-compatibility
COPY --from=cpa-download /out/cli-proxy-api /usr/local/bin/cli-proxy-api
COPY --from=cpa-download /out/cpa-artifact.json /usr/local/share/cpamp/runtime/cpa-artifact.json
COPY --from=v7-evidence /image/usr/local/share/licenses/cli-proxy-api/LICENSE /usr/local/share/licenses/cli-proxy-api/LICENSE
COPY --from=v7-evidence /image/usr/local/share/cpamp/ /usr/local/share/cpamp/
COPY docker/runtime/config.seed.yaml /usr/local/share/cpamp/runtime/config.seed.yaml

# Default v2 full bundle: slim + official pinned CPA v8.0.55.
FROM slim AS full
COPY --from=cpa-v8-download /out/cli-proxy-api /usr/local/bin/cli-proxy-api
COPY --from=full-evidence /image/usr/local/share/licenses/cli-proxy-api/LICENSE /usr/local/share/licenses/cli-proxy-api/LICENSE
COPY --from=full-evidence /image/usr/local/share/cpamp/ /usr/local/share/cpamp/
