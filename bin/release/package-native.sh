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
license_source_inventory="${repo_root}/bin/release/embedded-third-party-inventory.txt"

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

# Every member timestamp is pinned to the epoch, deepest path first so a
# directory is never re-stamped after its children. This is what makes the
# archive independent of when the staging tree happened to be written.
normalize_archive_times() {
  local member="$1"

  TZ=UTC find "${member}" -depth -exec touch -h -t 197001010000.00 {} +
}

# Native archives are extracted by root into /opt/cpa-manager-plus. GNU tar
# restores archived ownership, so a CI account's UID/GID would otherwise own
# the launcher systemd runs as root. Always record root:root and a fixed mtime.
#
# The archive must also be byte-identical across runs and tar implementations,
# so members are fed as a single C-sorted list (never in readdir order), macOS
# metadata is suppressed, and gzip writes no name/timestamp header.
tar_create_archive() {
  local archive="$1"
  local member="$2"
  local tar_version member_list

  if [ ! -e "${member}" ]; then
    echo "Refusing to archive a missing member: ${member}" >&2
    return 1
  fi

  normalize_archive_times "${member}"

  member_list="$(mktemp "${TMPDIR:-/tmp}/cpamp-native-members.XXXXXX")"
  find "${member}" -print | LC_ALL=C sort >"${member_list}"

  tar_version="$(tar --version 2>/dev/null || true)"
  if printf '%s' "${tar_version}" | grep -q 'GNU tar'; then
    # GNU's posix format records atime/ctime in extended headers by default, and
    # those change on every `touch`; delete them so only the pinned mtime remains.
    tar --owner=0 --group=0 --numeric-owner --sort=name --mtime=@0 --format=posix \
      --pax-option=delete=atime,delete=ctime --no-recursion \
      -cf - -T "${member_list}" | gzip -n >"${archive}"
  else
    COPYFILE_DISABLE=1 tar --uid 0 --gid 0 --uname root --gname root --no-mac-metadata \
      --no-recursion -cf - -T "${member_list}" | gzip -n >"${archive}"
  fi

  rm -f "${member_list}"
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

# The v2 Native package must carry the same evidence as the images: original
# license texts per linked component, an aggregate inventory, and provenance
# binding the packaged bytes. A missing original notice fails the build.
collect_native_licenses() {
  local package_dir="$1"
  local module_dir

  if ! command -v go >/dev/null 2>&1; then
    echo "go is required to download every declared Go module before license collection" >&2
    exit 1
  fi
  if ! command -v python3 >/dev/null 2>&1; then
    echo "python3 is required to collect Native third-party licenses" >&2
    exit 1
  fi

  # A Linux-amd64 build does not necessarily fetch every module declared in
  # go.mod, while the collector requires all of them. Mirror the image build
  # order and download the declared set first, against the resolved root.
  for module_dir in "${server_src}" "${ingress_src}" "${supervisor_src}"; do
    (
      cd "${module_dir}"
      GOROOT="${native_go_root}" go mod download
    )
  done

  GOROOT="${native_go_root}" python3 "${repo_root}/bin/release/collect-third-party-licenses.py" go \
    --module "${server_src}" --component manager \
    --output "${package_dir}/share/licenses/manager"
  GOROOT="${native_go_root}" python3 "${repo_root}/bin/release/collect-third-party-licenses.py" go \
    --module "${ingress_src}" --component ingress \
    --output "${package_dir}/share/licenses/ingress"
  GOROOT="${native_go_root}" python3 "${repo_root}/bin/release/collect-third-party-licenses.py" go \
    --module "${supervisor_src}" --component runtime-supervisor \
    --output "${package_dir}/share/licenses/runtime-supervisor"
  python3 "${repo_root}/bin/release/collect-third-party-licenses.py" web \
    --root "${repo_root}" --output "${package_dir}/share/licenses/web"

  native_source_inventory="${license_source_inventory}"
}

# Resolve the toolchain the collector actually reads. `go env GOROOT` is
# evaluated inside each module directory so a go.mod toolchain/GOTOOLCHAIN
# switch is honoured; <GOROOT>/VERSION is the authoritative statement of what
# that root contains, because GOVERSION reports the running tool, not the root.
resolve_native_go_root() {
  local module_dir goroot gover root_version expected_goroot="" expected_version=""

  for module_dir in "${server_src}" "${ingress_src}" "${supervisor_src}"; do
    if ! goroot="$(cd "${module_dir}" && go env GOROOT)"; then
      echo "Cannot resolve the Go toolchain in ${module_dir}" >&2
      exit 1
    fi
    if ! gover="$(cd "${module_dir}" && go env GOVERSION)"; then
      echo "Cannot resolve the Go version in ${module_dir}" >&2
      exit 1
    fi
    if [ -z "${goroot}" ] || [ -z "${gover}" ] || [ ! -f "${goroot}/VERSION" ]; then
      echo "Go toolchain in ${module_dir} has no readable GOROOT/VERSION (GOROOT=${goroot})" >&2
      exit 1
    fi
    root_version="$(sed -n '1p' "${goroot}/VERSION")"
    if [ "${root_version}" != "${gover}" ]; then
      echo "GOROOT ${goroot} contains ${root_version} but go env GOVERSION reports ${gover} in ${module_dir}" >&2
      exit 1
    fi
    if [ -n "${expected_goroot}" ] &&
      { [ "${goroot}" != "${expected_goroot}" ] || [ "${gover}" != "${expected_version}" ]; }; then
      echo "Go toolchains differ across module directories: ${expected_goroot} (${expected_version}) and ${goroot} (${gover})" >&2
      exit 1
    fi
    expected_goroot="${goroot}"
    expected_version="${gover}"
  done

  native_go_root="${expected_goroot}"
  native_go_version="${expected_version}"
}

# Every packaged binary must have been produced by the same resolved toolchain,
# otherwise the collector's standard-library notices describe code it does not
# contain.
verify_native_binary_toolchain() {
  local package_dir="$1"
  local binary version

  for binary in \
    cpa-manager-plus \
    cpa-manager-plus-ingress \
    cpa-manager-plus-runtime-supervisor; do
    if ! version="$(go version -m "${package_dir}/bin/${binary}" 2>/dev/null | awk 'NR == 1 {print $NF}')" ||
      [ -z "${version}" ]; then
      echo "Cannot read the Go toolchain embedded in bin/${binary}" >&2
      exit 1
    fi
    if [ "${version}" != "${native_go_version}" ]; then
      echo "bin/${binary} was built with ${version} but the collector toolchain is ${native_go_version}" >&2
      exit 1
    fi
  done
}

write_native_provenance() {
  local package_dir="$1"
  local source_commit="$2"

  if ! command -v python3 >/dev/null 2>&1; then
    echo "python3 is required to write Native provenance" >&2
    exit 1
  fi

  local unpinned_flag=""
  if [ "${native_cpa_official}" != "1" ]; then
    unpinned_flag="--cpa-unpinned"
  fi
  python3 "${repo_root}/bin/release/image-evidence.py" \
    --root "${package_dir}" \
    --role native \
    --version "${version}" \
    --source-commit "${source_commit}" \
    --platform linux/amd64 \
    --source-inventory "${native_source_inventory}" \
    ${unpinned_flag}
}

mkdir -p "${repo_root}/bin/tmp/release"
work_dir="$(mktemp -d "${repo_root}/bin/tmp/release/native.XXXXXX")"
trap 'rm -rf "${work_dir}"' EXIT
native_source_inventory=""
native_go_root=""
native_go_version=""
native_cpa_official=1

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
  local source_commit="${SOURCE_COMMIT:-$(git -C "${repo_root}" rev-parse HEAD)}"

  if [ -z "${cpa_version}" ] || [ -z "${cpa_executable_sha256}" ]; then
    echo "Failed to resolve pinned CPA v8 version/hash for linux/amd64" >&2
    exit 1
  fi

  # Provenance binds this exact commit; a moving or abbreviated ref would make
  # the packaged evidence unverifiable.
  if [[ ! "${source_commit}" =~ ^[0-9a-f]{40}$ ]]; then
    echo "SOURCE_COMMIT must be an exact 40-hex commit, got: ${source_commit}" >&2
    exit 1
  fi

  # The v2 package must be built from source: the script places the Full Web
  # dist into the embed tree itself, so the binary/manifest binding holds by
  # construction. A prebuilt binary's embedded file set cannot be proven from
  # its bytes, so any prebuilt input fails closed. The 1.x path is unaffected.
  if [ -n "${CPAMP_PREBUILT_MANAGER_BIN:-}" ] ||
    [ -n "${CPAMP_PREBUILT_INGRESS_BIN:-}" ] ||
    [ -n "${CPAMP_PREBUILT_RUNTIME_SUPERVISOR_BIN:-}" ]; then
    echo "v2 Native packaging builds Manager, Ingress and Runtime Supervisor from source; prebuilt binaries cannot be bound to the embedded Full Web file set" >&2
    exit 1
  fi

  # Resolve the toolchain before any build so a GOROOT that disagrees with the
  # running go command fails here, not as a confusing compile error.
  if ! command -v go >/dev/null 2>&1; then
    echo "go is required to build the v2 Native package" >&2
    exit 1
  fi
  resolve_native_go_root

  # The sha pin parsed from Dockerfile.runtime is the architecture and
  # plugin-build guarantee. An environment override is a different artifact, so
  # it needs an explicit, reviewable opt-in instead of silently shipping it. A
  # directly supplied binary skips the pinned archive and its upstream LICENSE,
  # so it is never official either, even when its digest matches the pin.
  if [ "${cpa_version}" != "${default_cpa_version}" ] ||
    [ "${cpa_archive_sha256}" != "${default_archive_sha}" ] ||
    [ "${cpa_executable_sha256}" != "${default_exec_sha}" ] ||
    [ "${cpa_asset_name}" != "CLIProxyAPI_${default_cpa_version}_linux_amd64.tar.gz" ] ||
    [ -n "${CPA_BINARY_PATH:-}" ]; then
    if [ "${CPAMP_ALLOW_UNPINNED_CPA:-0}" != "1" ]; then
      echo "CPA input differs from the Dockerfile.runtime linux/amd64 pinned archive; set CPAMP_ALLOW_UNPINNED_CPA=1 only for an explicitly reviewed artifact" >&2
      exit 1
    fi
    # Any deviation from the pin is local evidence only: it must never claim
    # the official CPA release or a verified status.
    native_cpa_official=0
    echo "WARNING: packaging a non-official CPA artifact; provenance will be labelled UNPINNED_LOCAL_CPA and is not release evidence" >&2
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
    "${package_dir}/share/cpamp/release" \
    "${package_dir}/share/licenses/cli-proxy-api" \
    "${package_dir}/share/licenses/cpamp"
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

  # Build the 3 Go binaries (Manager, Ingress, Runtime Supervisor) from source.
  if [ ! -f "${web_html}" ]; then
    echo "missing ${web_html}; run npm run build first" >&2
    exit 1
  fi
  # The v2 Native package embeds Full Web; its license inventory must cover
  # the exact production dependency closure those bytes came from.
  if [ ! -d "${full_web_dist}" ]; then
    echo "missing ${full_web_dist}; the v2 Native package embeds Full Web, run npm run build first" >&2
    exit 1
  fi
  if [ ! -f "${repo_root}/bin/release/hash-fullweb.mjs" ] || ! command -v node >/dev/null 2>&1; then
    echo "node and bin/release/hash-fullweb.mjs are required to bind the embedded Full Web build" >&2
    exit 1
  fi

  cp -R "${server_src}" "${work_dir}/manager-server"
  cp "${web_html}" "${work_dir}/manager-server/internal/httpapi/web/management.html"
  if [ -f "${repo_root}/tests/fixtures/r1-embedded-api-transport.json" ]; then
    cp "${repo_root}/tests/fixtures/r1-embedded-api-transport.json" \
      "${work_dir}/manager-server/internal/domain/routing/release-embedded-api-transport.json"
  fi
  # The embed tree must contain exactly the Full Web dist. The repository keeps
  # a .gitkeep placeholder there, which would otherwise ship as an extra file
  # that the manifest does not list (Docker avoids it via .dockerignore).
  rm -rf "${work_dir}/manager-server/internal/httpapi/web/fullweb"
  mkdir -p "${work_dir}/manager-server/internal/httpapi/web/fullweb"
  cp -R "${full_web_dist}/." "${work_dir}/manager-server/internal/httpapi/web/fullweb/"
  node "${repo_root}/bin/release/hash-fullweb.mjs" \
    "${full_web_dist}" \
    "${work_dir}/manager-server/internal/httpapi/release-fullweb-manifest.json"
  cp "${work_dir}/manager-server/internal/httpapi/release-fullweb-manifest.json" \
    "${package_dir}/share/cpamp/release/fullweb-manifest.json"

  # Same guarantee the image build enforces: the staged embed must match the
  # manifest exactly before the binary is compiled.
  (
    cd "${work_dir}/manager-server"
    CPAMP_FULLWEB_PACKAGED_TEST=1 go test -run '^TestPackagedFullWeb(Dist|Manifest)$' ./internal/httpapi
  )

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

  chmod 0755 \
    "${package_dir}/bin/cpa-manager-plus" \
    "${package_dir}/bin/cpa-manager-plus-ingress" \
    "${package_dir}/bin/cpa-manager-plus-runtime-supervisor"

  # Native license and provenance evidence, mirroring the image contract.
  cp "${repo_root}/LICENSE" "${package_dir}/share/licenses/cpamp/LICENSE"
  chmod 0444 "${package_dir}/share/licenses/cpamp/LICENSE"
  if [ ! -s "${package_dir}/share/cpamp/release/fullweb-manifest.json" ]; then
    echo "Missing share/cpamp/release/fullweb-manifest.json: the v2 Native package must be built from source with the embedded Full Web dist" >&2
    exit 1
  fi
  verify_native_binary_toolchain "${package_dir}"
  collect_native_licenses "${package_dir}"
  write_native_provenance "${package_dir}" "${source_commit}"

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
