#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
version="${VERSION:-dev}"
out_dir="${OUT_DIR:-"${repo_root}/dist/native"}"
web_html="${WEB_HTML:-"${repo_root}/apps/web/dist/panel/management.html"}"
full_web_dist="${FULL_WEB_DIST:-"${repo_root}/apps/web/dist/fullweb"}"
binary_name="cpa-manager-plus"
server_src="${repo_root}/apps/manager-server"
ingress_src="${repo_root}/apps/ingress"
supervisor_src="${repo_root}/apps/runtime-supervisor"
native_script_src="${repo_root}/bin/native"
runtime_dockerfile="${repo_root}/Dockerfile.runtime"

if [ -n "${PACKAGE_MODE:-}" ]; then
  package_mode="${PACKAGE_MODE}"
elif [[ "${version}" =~ ^v1\. ]]; then
  package_mode="v1"
else
  package_mode="v2"
fi

compute_sha256() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${file}" | awk '{print $1}'
  else
    shasum -a 256 "${file}" | awk '{print $1}'
  fi
}

# Native archives are extracted by root into /opt/cpa-manager-plus. GNU tar
# restores archived ownership, so a CI account's UID/GID would otherwise own
# the launcher systemd runs as root. Always record root:root and a fixed mtime.
tar_create_archive() {
  local archive="$1"
  local member="$2"
  local tar_version

  tar_version="$(tar --version 2>/dev/null || true)"
  if printf '%s' "${tar_version}" | grep -q 'GNU tar'; then
    tar --owner=0 --group=0 --numeric-owner --sort=name --mtime=@0 -czf "${archive}" "${member}"
  else
    tar --uid 0 --gid 0 --uname root --gname root -czf "${archive}" "${member}"
  fi
}

parse_dockerfile_cpa_v8_pin() {
  local field="$1"
  awk -v field="${field}" '
    /^FROM .* AS cpa-v8-download/ { in_stage = 1; next }
    in_stage && /^FROM / { in_stage = 0 }
    in_stage && /amd64\)/ { in_amd64 = 1 }
    in_stage && /arm64\)/ { in_amd64 = 0 }
    in_stage && field == "version" && /asset_name="CLIProxyAPI_[^_]+_linux_/ {
      line = $0
      sub(/.*asset_name="CLIProxyAPI_/, "", line)
      sub(/_linux_.*/, "", line)
      print line
      exit
    }
    in_stage && in_amd64 && field == "archive_sha256" && /archive_sha256="/ {
      line = $0
      sub(/.*archive_sha256="/, "", line)
      sub(/".*/, "", line)
      print line
      exit
    }
    in_stage && in_amd64 && field == "executable_sha256" && /executable_sha256="/ {
      line = $0
      sub(/.*executable_sha256="/, "", line)
      sub(/".*/, "", line)
      print line
      exit
    }
  ' "${runtime_dockerfile}"
}

mkdir -p "${repo_root}/bin/tmp/release"
work_dir="$(mktemp -d "${repo_root}/bin/tmp/release/native.XXXXXX")"
trap 'rm -rf "${work_dir}"' EXIT

rm -rf "${out_dir}"
mkdir -p "${out_dir}"

package_v2_linux_amd64() {
  local package_prefix="${CPAMP_NATIVE_V2_PREFIX:-cpa-manager-plus-native-v2}"
  local package_name="${package_prefix}_${version}_linux_amd64"
  local archive_name="${package_name}.tar.gz"
  local package_dir="${work_dir}/${package_name}"

  case "${archive_name}" in
    cpa-manager-plus_v*_* | release-info.json)
      echo "Refusing v2 native asset name that collides with 1.x schema-1 assets: ${archive_name}" >&2
      exit 1
      ;;
  esac

  local default_cpa_version default_archive_sha default_exec_sha
  default_cpa_version="$(parse_dockerfile_cpa_v8_pin version)"
  default_archive_sha="$(parse_dockerfile_cpa_v8_pin archive_sha256)"
  default_exec_sha="$(parse_dockerfile_cpa_v8_pin executable_sha256)"

  local cpa_version="${CPAMP_CPA_VERSION:-${default_cpa_version}}"
  local cpa_archive_sha256="${CPAMP_CPA_ARCHIVE_SHA256:-${default_archive_sha}}"
  local cpa_executable_sha256="${CPAMP_CPA_EXECUTABLE_SHA256:-${default_exec_sha}}"
  local cpa_asset_name="${CPAMP_CPA_ASSET_NAME:-CLIProxyAPI_${cpa_version}_linux_amd64.tar.gz}"

  if [ -z "${cpa_version}" ] || [ -z "${cpa_executable_sha256}" ]; then
    echo "Failed to resolve pinned CPA v8 version/hash for linux/amd64" >&2
    exit 1
  fi

  # The sha pin parsed from Dockerfile.runtime is the architecture and
  # plugin-build guarantee. An environment override is a different artifact, so
  # it needs an explicit, reviewable opt-in instead of silently shipping it.
  if [ "${cpa_version}" != "${default_cpa_version}" ] ||
    [ "${cpa_archive_sha256}" != "${default_archive_sha}" ] ||
    [ "${cpa_executable_sha256}" != "${default_exec_sha}" ] ||
    [ "${cpa_asset_name}" != "CLIProxyAPI_${default_cpa_version}_linux_amd64.tar.gz" ]; then
    if [ "${CPAMP_ALLOW_UNPINNED_CPA:-0}" != "1" ]; then
      echo "CPA input differs from the Dockerfile.runtime linux/amd64 pin; set CPAMP_ALLOW_UNPINNED_CPA=1 only for an explicitly reviewed artifact" >&2
      exit 1
    fi
  fi

  case "${cpa_asset_name}${CPA_BINARY_PATH:-}${CPA_ARCHIVE_PATH:-}" in
    *_no-plugin*)
      echo "Refusing _no-plugin CPA artifact for v2 native package" >&2
      exit 1
      ;;
  esac

  mkdir -p \
    "${package_dir}/bin" \
    "${package_dir}/data" \
    "${package_dir}/run" \
    "${package_dir}/logs" \
    "${package_dir}/share/cpamp/runtime" \
    "${package_dir}/share/licenses/cli-proxy-api"
  chmod 0700 "${package_dir}/data" "${package_dir}/run" "${package_dir}/logs"

  # Stage and verify pinned CPA v8 linux/amd64 binary
  if [ -n "${CPA_BINARY_PATH:-}" ]; then
    if [ ! -f "${CPA_BINARY_PATH}" ]; then
      echo "Missing CPA_BINARY_PATH: ${CPA_BINARY_PATH}" >&2
      exit 1
    fi
    if [ -z "${CPA_LICENSE_PATH:-}" ] || [ ! -s "${CPA_LICENSE_PATH}" ]; then
      echo "CPA_LICENSE_PATH is required with CPA_BINARY_PATH: the repository LICENSE is not CLIProxyAPI's license" >&2
      exit 1
    fi
    local actual_exec_sha
    actual_exec_sha="$(compute_sha256 "${CPA_BINARY_PATH}")"
    if [ "${actual_exec_sha}" != "${cpa_executable_sha256}" ]; then
      echo "Pinned CPA v8 executable SHA-256 mismatch: expected ${cpa_executable_sha256}, got ${actual_exec_sha}" >&2
      exit 1
    fi
    cp "${CPA_BINARY_PATH}" "${package_dir}/bin/cli-proxy-api"
    chmod 0755 "${package_dir}/bin/cli-proxy-api"
    cp "${CPA_LICENSE_PATH}" "${package_dir}/share/licenses/cli-proxy-api/LICENSE"
    chmod 0444 "${package_dir}/share/licenses/cli-proxy-api/LICENSE"
  else
    local cpa_archive="${CPA_ARCHIVE_PATH:-${work_dir}/${cpa_asset_name}}"
    if [ -z "${CPA_ARCHIVE_PATH:-}" ]; then
      local cpa_url="https://github.com/router-for-me/CLIProxyAPI/releases/download/v${cpa_version}/${cpa_asset_name}"
      if command -v curl >/dev/null 2>&1; then
        curl -fsSL -o "${cpa_archive}" "${cpa_url}"
      elif command -v wget >/dev/null 2>&1; then
        wget -qO "${cpa_archive}" "${cpa_url}"
      else
        echo "Neither curl nor wget is available to download ${cpa_url}" >&2
        exit 1
      fi
    fi
    if [ ! -f "${cpa_archive}" ]; then
      echo "Missing CPA archive: ${cpa_archive}" >&2
      exit 1
    fi
    local actual_archive_sha
    actual_archive_sha="$(compute_sha256 "${cpa_archive}")"
    if [ "${actual_archive_sha}" != "${cpa_archive_sha256}" ]; then
      echo "Pinned CPA v8 archive SHA-256 mismatch: expected ${cpa_archive_sha256}, got ${actual_archive_sha}" >&2
      exit 1
    fi
    tar -xOzf "${cpa_archive}" cli-proxy-api >"${package_dir}/bin/cli-proxy-api"
    local actual_exec_sha
    actual_exec_sha="$(compute_sha256 "${package_dir}/bin/cli-proxy-api")"
    if [ "${actual_exec_sha}" != "${cpa_executable_sha256}" ]; then
      echo "Pinned CPA v8 executable SHA-256 mismatch: expected ${cpa_executable_sha256}, got ${actual_exec_sha}" >&2
      exit 1
    fi
    tar -xOzf "${cpa_archive}" LICENSE >"${package_dir}/share/licenses/cli-proxy-api/LICENSE"
    test -s "${package_dir}/share/licenses/cli-proxy-api/LICENSE"
    chmod 0755 "${package_dir}/bin/cli-proxy-api"
    chmod 0444 "${package_dir}/share/licenses/cli-proxy-api/LICENSE"
  fi

  printf '{"schemaVersion":1,"engine":"cpa","version":"%s","artifactId":"sha256:%s"}\n' \
    "${cpa_version}" "${cpa_executable_sha256}" >"${package_dir}/share/cpamp/runtime/cpa-artifact.json"
  chmod 0444 "${package_dir}/share/cpamp/runtime/cpa-artifact.json"

  if [ -f "${repo_root}/docker/runtime/config.v8.default.seed.yaml" ]; then
    cp "${repo_root}/docker/runtime/config.v8.default.seed.yaml" "${package_dir}/share/cpamp/runtime/config.seed.yaml"
    chmod 0644 "${package_dir}/share/cpamp/runtime/config.seed.yaml"
  fi

  # Build or copy the 3 Go binaries (Manager, Ingress, Runtime Supervisor)
  if [ -n "${CPAMP_PREBUILT_MANAGER_BIN:-}" ] &&
    [ -n "${CPAMP_PREBUILT_INGRESS_BIN:-}" ] &&
    [ -n "${CPAMP_PREBUILT_RUNTIME_SUPERVISOR_BIN:-}" ]; then
    for prebuilt in \
      "${CPAMP_PREBUILT_MANAGER_BIN}" \
      "${CPAMP_PREBUILT_INGRESS_BIN}" \
      "${CPAMP_PREBUILT_RUNTIME_SUPERVISOR_BIN}"; do
      if [ ! -s "${prebuilt}" ]; then
        echo "Missing or empty prebuilt binary: ${prebuilt}" >&2
        exit 1
      fi
    done
    cp "${CPAMP_PREBUILT_MANAGER_BIN}" "${package_dir}/bin/cpa-manager-plus"
    cp "${CPAMP_PREBUILT_INGRESS_BIN}" "${package_dir}/bin/cpa-manager-plus-ingress"
    cp "${CPAMP_PREBUILT_RUNTIME_SUPERVISOR_BIN}" "${package_dir}/bin/cpa-manager-plus-runtime-supervisor"
  else
    if [ ! -f "${web_html}" ]; then
      echo "missing ${web_html}; run npm run build first" >&2
      exit 1
    fi
    local source_commit
    source_commit="${SOURCE_COMMIT:-$(git -C "${repo_root}" rev-parse HEAD)}"

    cp -R "${server_src}" "${work_dir}/manager-server"
    cp "${web_html}" "${work_dir}/manager-server/internal/httpapi/web/management.html"
    if [ -f "${repo_root}/tests/fixtures/r1-embedded-api-transport.json" ]; then
      cp "${repo_root}/tests/fixtures/r1-embedded-api-transport.json" \
        "${work_dir}/manager-server/internal/domain/routing/release-embedded-api-transport.json"
    fi
    if [ -d "${full_web_dist}" ]; then
      cp -R "${full_web_dist}/." "${work_dir}/manager-server/internal/httpapi/web/fullweb/"
      if [ -f "${repo_root}/bin/release/hash-fullweb.mjs" ] && command -v node >/dev/null 2>&1; then
        node "${repo_root}/bin/release/hash-fullweb.mjs" \
          "${full_web_dist}" \
          "${work_dir}/manager-server/internal/httpapi/release-fullweb-manifest.json"
      fi
    fi

    (
      cd "${work_dir}/manager-server"
      CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
        -ldflags "-s -w -X github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo.Version=${version} -X github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo.SourceCommit=${source_commit}" \
        -o "${package_dir}/bin/cpa-manager-plus" ./cmd/cpa-manager-plus
    )
    (
      cd "${ingress_src}"
      CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" \
        -o "${package_dir}/bin/cpa-manager-plus-ingress" ./cmd/cpamp-ingress
    )
    (
      cd "${supervisor_src}"
      CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" \
        -o "${package_dir}/bin/cpa-manager-plus-runtime-supervisor" ./cmd/cpamp-runtime-supervisor
    )
  fi

  chmod 0755 \
    "${package_dir}/bin/cpa-manager-plus" \
    "${package_dir}/bin/cpa-manager-plus-ingress" \
    "${package_dir}/bin/cpa-manager-plus-runtime-supervisor"

  # Install launcher, control script, systemd unit, and docs
  cp "${native_script_src}/cpa-manager-plus-launcher" "${package_dir}/bin/cpa-manager-plus-launcher"
  cp "${native_script_src}/cpa-manager-plus-launcher" "${package_dir}/cpa-manager-plus-launcher"
  cp "${native_script_src}/cpa-manager-plusctl.sh" "${package_dir}/cpa-manager-plusctl"
  cp "${native_script_src}/cpa-manager-plus.service" "${package_dir}/cpa-manager-plus.service"
  chmod 0755 \
    "${package_dir}/bin/cpa-manager-plus-launcher" \
    "${package_dir}/cpa-manager-plus-launcher" \
    "${package_dir}/cpa-manager-plusctl"
  chmod 0644 "${package_dir}/cpa-manager-plus.service"

  cp "${repo_root}/README.md" "${package_dir}/README.md"
  cp "${repo_root}/README_CN.md" "${package_dir}/README_CN.md"
  if [ "${CPAMP_SKIP_PACKAGE_DOCS:-0}" != "1" ]; then
    cp -R "${repo_root}/docs" "${package_dir}/docs"
  else
    mkdir -p "${package_dir}/docs"
  fi
  cp "${repo_root}/LICENSE" "${package_dir}/LICENSE"

  (
    cd "${work_dir}"
    tar_create_archive "${out_dir}/${archive_name}" "${package_name}"
  )
}

package_v1_matrix() {
  if [ ! -f "${web_html}" ]; then
    echo "missing ${web_html}; run npm run build first" >&2
    exit 1
  fi

  cp -R "${server_src}" "${work_dir}/manager-server"
  cp "${web_html}" "${work_dir}/manager-server/internal/httpapi/web/management.html"
  if [ -d "${full_web_dist}" ]; then
    cp -R "${full_web_dist}/." "${work_dir}/manager-server/internal/httpapi/web/fullweb/"
  fi

  local targets=(
    "linux amd64"
    "linux arm64"
    "darwin amd64"
    "darwin arm64"
    "windows amd64"
    "windows arm64"
  )

  local target goos goarch package_name package_dir exe_name
  for target in "${targets[@]}"; do
    read -r goos goarch <<<"${target}"
    package_name="${binary_name}_${version}_${goos}_${goarch}"
    package_dir="${work_dir}/${package_name}"
    exe_name="${binary_name}"

    if [ "${goos}" = "windows" ]; then
      exe_name="${binary_name}.exe"
    fi

    mkdir -p "${package_dir}"
    (
      cd "${work_dir}/manager-server"
      CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" go build -trimpath -ldflags "-s -w -X github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo.Version=${version} -X github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo.SourceCommit=$(git -C "${repo_root}" rev-parse HEAD)" -o "${package_dir}/${exe_name}" ./cmd/cpa-manager-plus
    )

    cp "${repo_root}/README.md" "${package_dir}/README.md"
    cp "${repo_root}/README_CN.md" "${package_dir}/README_CN.md"
    cp -R "${repo_root}/docs" "${package_dir}/docs"
    cp "${repo_root}/LICENSE" "${package_dir}/LICENSE"
    if [ "${goos}" = "windows" ]; then
      cp "${native_script_src}/cpa-manager-plusctl.ps1" "${package_dir}/cpa-manager-plusctl.ps1"
    else
      cp "${native_script_src}/cpa-manager-plusctl.sh" "${package_dir}/cpa-manager-plusctl"
      chmod 0755 "${package_dir}/cpa-manager-plusctl"
    fi

    if [ "${goos}" = "windows" ]; then
      (
        cd "${work_dir}"
        zip -qr "${out_dir}/${package_name}.zip" "${package_name}"
      )
    else
      (
        cd "${work_dir}"
        tar_create_archive "${out_dir}/${package_name}.tar.gz" "${package_name}"
      )
    fi
  done
}

case "${package_mode}" in
  v2)
    package_v2_linux_amd64
    ;;
  v1)
    package_v1_matrix
    ;;
  *)
    echo "Unsupported PACKAGE_MODE: ${package_mode} (expected v2 or v1)" >&2
    exit 1
    ;;
esac

(
  cd "${out_dir}"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum ./* > checksums.txt
  else
    shasum -a 256 ./* > checksums.txt
  fi
)
