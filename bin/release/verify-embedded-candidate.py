#!/usr/bin/env python3
"""Verify a disposable, local-only OCI Embedded trio. Never publishes or signs a release.

Input archives must be produced by one explicitly identified candidate build event.
A self-asserted build event is NOT protected-producer provenance.
"""
import argparse
import hashlib
import io
import importlib.util
import gzip
import json
import os
import re
import shlex
import struct
import subprocess
import sys
import tarfile
import tempfile
from pathlib import Path, PurePosixPath

ROLES = ("manager", "ingress", "runtime")
SHA = re.compile(r"^sha256:[0-9a-f]{64}$")
SOURCE = "https://github.com/seakee/CPA-Manager-Plus"
CANDIDATE_VERSION = "2.0.0-r1candidate"
evidence_spec = importlib.util.spec_from_file_location("image_evidence", Path(__file__).with_name("image-evidence.py"))
image_evidence = importlib.util.module_from_spec(evidence_spec)
evidence_spec.loader.exec_module(image_evidence)
CPA_SHA = "e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7"
CPA_LICENSE_SHA = "879792e89cf1bdd6a8d446033ec87e30496f97dcafc4656dc53f641509b346a6"
CPA_ARCHIVE_SHA = "4cd593251db85e50c820f42e6ea5cecdd05f902201d3dbdb025671d4f306a123"
LICENSE = "/usr/local/share/licenses/cpamp/LICENSE"
NOTICE = "/usr/local/share/cpamp/release/third-party-inventory.txt"
WEB_MANIFEST = "/usr/local/share/cpamp/release/fullweb-manifest.json"
API_CONTRACT = "/usr/local/share/cpamp/release/public-api-transport-contract.json"
API_CONTRACT_QUALIFICATION = "SOURCE_TRANSPORT_CONTRACT_ONLY_NOT_COMPLETE_WEB_API_COMPATIBILITY"
WANTED = {
    "manager": ("/usr/local/bin/cpa-manager-plus", LICENSE, NOTICE, WEB_MANIFEST, API_CONTRACT),
    "ingress": ("/usr/local/bin/cpamp-ingress", LICENSE, NOTICE),
    "runtime": (
        "/usr/local/bin/cpamp-runtime-supervisor",
        "/usr/local/bin/cli-proxy-api",
        "/usr/local/share/cpamp/runtime/cpa-artifact.json",
        "/usr/local/share/cpamp/runtime/config.seed.yaml",
        "/usr/local/bin/cpamp-runtime-v8-entrypoint",
        "/usr/local/share/licenses/cli-proxy-api/LICENSE",
        LICENSE, NOTICE,
    ),
}
WANTED = {role: paths + (image_evidence.PROVENANCE, image_evidence.SOURCE_INVENTORY)
          for role, paths in WANTED.items()}
MAX_BLOB = 512 * 1024 * 1024
MAX_FILE = 128 * 1024 * 1024
MAX_UNCOMPRESSED_LAYER = 768 * 1024 * 1024
GO_MODULES = {
    "manager": ("github.com/seakee/cpa-manager-plus/apps/manager-server", "cmd/cpa-manager-plus"),
    "ingress": ("github.com/seakee/cpa-manager-plus/apps/ingress", "cmd/cpamp-ingress"),
    "runtime": ("github.com/seakee/cpa-manager-plus/apps/runtime-supervisor", "cmd/cpamp-runtime-supervisor"),
}
GO_BINARIES = {
    "manager": "/usr/local/bin/cpa-manager-plus",
    "ingress": "/usr/local/bin/cpamp-ingress",
    "runtime": "/usr/local/bin/cpamp-runtime-supervisor",
}
RUNTIME_ENTRYPOINT = "/usr/local/bin/cpamp-runtime-v8-entrypoint"
# Exact startup contract of the three R1-1 Dockerfile final stages.
STARTUP = {
    "manager": (["cpa-manager-plus"], None),
    "ingress": (["/usr/local/bin/cpamp-ingress"], None),
    "runtime": ([RUNTIME_ENTRYPOINT], ["/usr/local/bin/cpamp-runtime-supervisor"]),
}
MANIFEST_TYPE = "application/vnd.oci.image.manifest.v1+json"
CONFIG_TYPE = "application/vnd.oci.image.config.v1+json"
LAYER_GZIP = "application/vnd.oci.image.layer.v1.tar+gzip"
LAYER_TAR = "application/vnd.oci.image.layer.v1.tar"


def digest(data):
    return "sha256:" + hashlib.sha256(data).hexdigest()


def require(condition, message):
    if not condition:
        raise ValueError(message)


def json_object(data, label):
    result = json.loads(data)
    require(type(result) is dict, "expected JSON object: " + label)
    return result


def read_member(archive, name, limit=MAX_BLOB):
    item = archive.getmember(name)
    require(item.isfile() and item.size <= limit, "invalid archive member " + name)
    return archive.extractfile(item).read()


def read_blob(archive, identifier):
    require(isinstance(identifier, str) and SHA.fullmatch(identifier), "invalid OCI digest")
    data = read_member(archive, "blobs/sha256/" + identifier[7:])
    require(digest(data) == identifier, "OCI blob digest mismatch: " + identifier)
    return data



def read_descriptor(archive, descriptor, allowed_types):
    require(type(descriptor) is dict, "missing OCI descriptor")
    require(descriptor.get("mediaType") in allowed_types, "unexpected OCI descriptor mediaType")
    expected_size = descriptor.get("size")
    require(type(expected_size) is int and 0 < expected_size <= MAX_BLOB,
            "invalid OCI descriptor size")
    raw = read_blob(archive, descriptor.get("digest"))
    require(len(raw) == expected_size, "OCI descriptor size mismatch")
    return raw


def uncompressed_layer(raw, media_type):
    if media_type == LAYER_TAR:
        require(len(raw) <= MAX_UNCOMPRESSED_LAYER, "uncompressed layer is too large")
        return raw
    require(media_type == LAYER_GZIP, "unsupported OCI layer encoding")
    with gzip.GzipFile(fileobj=io.BytesIO(raw)) as stream:
        result = stream.read(MAX_UNCOMPRESSED_LAYER + 1)
    require(len(result) <= MAX_UNCOMPRESSED_LAYER, "uncompressed layer is too large")
    return result


def canonical_path(value):
    require(isinstance(value, str) and not value.startswith("/"), "unsafe layer entry")
    # OCI tar paths are relative to root; do not accept parent traversal.
    parts = PurePosixPath(value).parts
    require(".." not in parts, "unsafe layer traversal")
    while parts and parts[0] == ".":
        parts = parts[1:]
    return "/" + "/".join(parts)


def check_amd64_elf(data, name):
    # Only the linux/amd64 ELF64 format required by R1-1; no generic ELF parser.
    require(len(data) >= 64 and data[:7] == b"\x7fELF\x02\x01\x01",
            "invalid ELF64 header: " + name)
    elf_type, machine, version = struct.unpack_from("<HHI", data, 16)
    require(elf_type in (2, 3) and machine == 62 and version == 1,
            "binary is not linux/amd64 ELF: " + name)
    entry, phoff = struct.unpack_from("<QQ", data, 24)
    phentsize, phnum = struct.unpack_from("<HH", data, 54)
    require(phnum > 0 and phnum <= 256 and phentsize >= 56 and
            phoff >= 64 and phoff + phentsize * phnum <= len(data),
            "invalid ELF program headers: " + name)
    executable_load = False
    for index in range(phnum):
        offset = phoff + index * phentsize
        kind, flags = struct.unpack_from("<II", data, offset)
        file_offset, virtual_address = struct.unpack_from("<QQ", data, offset + 8)
        file_size, mem_size = struct.unpack_from("<QQ", data, offset + 32)
        require(file_size <= mem_size and file_offset + file_size <= len(data),
                "invalid ELF segment bounds: " + name)
        if kind == 1 and flags & 1 and file_size > 0 and (
                virtual_address <= entry < virtual_address + mem_size):
            executable_load = True
    require(executable_load, "ELF has no executable entry segment: " + name)


def check_manager_linker_identity(raw_flags, module, source_sha):
    require(isinstance(raw_flags, str) and raw_flags, "Manager linker flags missing")
    try:
        tokens = shlex.split(raw_flags)
        # Go metadata records the entire -ldflags argument as a quoted string.
        if len(tokens) == 1 and tokens[0].startswith("-X "):
            tokens = shlex.split(tokens[0])
    except ValueError as exc:
        raise ValueError("invalid Manager linker flag quoting") from exc
    assignments = {}
    for index in range(0, len(tokens), 2):
        require(index + 1 < len(tokens) and tokens[index] == "-X",
                "unexpected Manager linker flag")
        key, separator, value = tokens[index + 1].partition("=")
        require(separator == "=" and key and key not in assignments,
                "invalid or duplicate Manager linker assignment")
        assignments[key] = value
    expected = {
        module + "/internal/buildinfo.SourceCommit": source_sha,
        module + "/internal/buildinfo.Version": CANDIDATE_VERSION,
    }
    require(assignments == expected, "Manager compiled source/version stamp mismatch")


def check_startup_config(config, role):
    runtime = config.get("config")
    require(type(runtime) is dict, "OCI image runtime config missing")
    entrypoint, command = STARTUP[role]
    require(runtime.get("Entrypoint") == entrypoint,
            "final image Entrypoint/Cmd mismatch: " + role)
    if command is None:
        require(runtime.get("Cmd") in (None, []),
                "final image Entrypoint/Cmd mismatch: " + role)
    else:
        require(runtime.get("Cmd") == command,
                "final image Entrypoint/Cmd mismatch: " + role)


def check_go_binary(data, role, source_sha):
    # 'go version -m' reads compiled Go buildinfo without running the candidate.
    # The module/path are derived from the ELF, not OCI labels or file names.
    with tempfile.TemporaryDirectory(prefix="cpamp-r1-go-meta-") as directory:
        file = Path(directory) / "candidate"
        file.write_bytes(data)
        try:
            result = subprocess.run(["go", "version", "-m", str(file)],
                                    capture_output=True, text=True, timeout=20, check=False)
        except (OSError, subprocess.TimeoutExpired) as exc:
            raise ValueError("Go binary metadata inspection unavailable: " + role) from exc
    require(result.returncode == 0, "unreadable Go build metadata: " + role)
    parsed = {}
    options = {}
    for line in result.stdout.splitlines():
        parts = line.strip().split("\t")
        if len(parts) < 2:
            continue
        if parts[0] in ("path", "mod"):
            parsed[parts[0]] = parts[1]
        elif parts[0] == "build" and "=" in parts[1]:
            key, value = parts[1].split("=", 1)
            options[key] = value
    module, command = GO_MODULES[role]
    require(parsed.get("mod") == module and
            parsed.get("path") == module + "/" + command,
            "Go compiled module/entrypoint mismatch: " + role)
    require(options.get("GOOS") == "linux" and options.get("GOARCH") == "amd64" and
            options.get("CGO_ENABLED") == "0",
            "Go compiled target mismatch: " + role)
    if role == "manager":
        check_manager_linker_identity(options.get("-ldflags", ""), module, source_sha)
    return {"module": module, "entrypoint": parsed["path"],
            "goTarget": "linux/amd64",
            "managerCompiledSourceClaim": source_sha if role == "manager" else None}


def final_files(archive, layers, diff_ids, wanted):
    result = {}
    permissions = {}
    require(len(layers) == len(diff_ids), "OCI layer/diff_ids count mismatch")
    for descriptor, diff_id in zip(layers, diff_ids):
        raw = read_descriptor(archive, descriptor, (LAYER_TAR, LAYER_GZIP))
        unpacked = uncompressed_layer(raw, descriptor["mediaType"])
        require(digest(unpacked) == diff_id, "OCI rootfs diff_id mismatch")
        with tarfile.open(fileobj=io.BytesIO(unpacked), mode="r:") as layer:
            for member in layer:
                name = canonical_path(member.name)
                base = PurePosixPath(name).name
                parent = str(PurePosixPath(name).parent)
                if base == ".wh..wh..opq":
                    for target in list(result):
                        if target.startswith(parent.rstrip("/") + "/"):
                            del result[target]
                            permissions.pop(target, None)
                    continue
                if base.startswith(".wh."):
                    removed = parent.rstrip("/") + "/" + base[4:]
                    for target in list(result):
                        if target == removed or target.startswith(removed + "/"):
                            del result[target]
                            permissions.pop(target, None)
                    continue
                if name not in wanted and (not name.startswith(image_evidence.LICENSE_ROOT) or member.isdir()):
                    continue
                require(member.isfile() and member.size <= MAX_FILE,
                        "missing regular file in final image: " + name)
                stream = layer.extractfile(member)
                require(stream is not None, "unreadable final image file: " + name)
                result[name] = stream.read()
                permissions[name] = member.mode
    require(set(wanted) <= set(result), "missing packaged files: " +
            ", ".join(sorted(set(wanted) - set(result))))
    return result, permissions

def inspect(path, role, source_sha, event, root_license, seed, runtime_entrypoint):
    require(path.is_file(), "missing OCI archive: " + str(path))
    with tarfile.open(path, mode="r:*") as archive:
        layout = json_object(read_member(archive, "oci-layout", 4096), "oci-layout")
        require(layout.get("imageLayoutVersion") == "1.0.0", "not an OCI layout")
        raw_index = read_member(archive, "index.json", 1024 * 1024)
        index = json_object(raw_index, "index")
        require(index.get("schemaVersion") == 2, "invalid OCI index schema")
        descriptors = index.get("manifests")
        require(isinstance(descriptors, list), "missing OCI index descriptors")
        matches = [m for m in descriptors if type(m) is dict and
                   type(m.get("platform")) is dict and
                   m["platform"].get("os") == "linux" and
                   m["platform"].get("architecture") == "amd64"]
        # R1-1 exports one platform without provenance side manifests.
        require(len(matches) == 1 and len(descriptors) == 1,
                "candidate must contain exactly one linux/amd64 manifest")
        descriptor = matches[0]
        manifest_id = descriptor.get("digest")
        manifest = json_object(read_descriptor(archive, descriptor, (MANIFEST_TYPE,)), "manifest")
        require(manifest.get("schemaVersion") == 2 and
                manifest.get("mediaType", MANIFEST_TYPE) == MANIFEST_TYPE,
                "invalid OCI manifest schema or mediaType")
        config_descriptor = manifest.get("config")
        config = json_object(read_descriptor(archive, config_descriptor, (CONFIG_TYPE,)), "config")
        config_id = config_descriptor["digest"]
        require(config.get("os") == "linux" and config.get("architecture") == "amd64",
                "image config architecture mismatch")
        check_startup_config(config, role)
        layers = manifest.get("layers")
        require(isinstance(layers, list) and len(layers) > 0, "missing image layers")
        rootfs = config.get("rootfs")
        require(type(rootfs) is dict and rootfs.get("type") == "layers" and
                isinstance(rootfs.get("diff_ids"), list) and
                len(rootfs["diff_ids"]) == len(layers) and
                all(isinstance(d, str) and SHA.fullmatch(d) for d in rootfs["diff_ids"]),
                "OCI config rootfs.diff_ids missing or invalid")
        labels = (config.get("config") or {}).get("Labels") or {}
        require(labels.get("org.opencontainers.image.revision") == source_sha,
                "source identity label mismatch")
        require(labels.get("org.opencontainers.image.source") == SOURCE,
                "source repository mismatch")
        require(labels.get("org.opencontainers.image.version") == CANDIDATE_VERSION,
                "candidate version identity mismatch")
        require(labels.get("io.cpamp.candidate.event") == event and
                labels.get("io.cpamp.candidate.role") == role,
                "candidate build event or role mismatch")
        files, modes = final_files(archive, layers, rootfs["diff_ids"], WANTED[role])
        require(files[LICENSE] == root_license, "CPAMP LICENSE differs from source")
        inventory = json_object(files[NOTICE], "third-party inventory")
        image_evidence.validate_inventory(inventory, role, files.__getitem__)
        provenance = json_object(files[image_evidence.PROVENANCE], "component provenance")
        image_evidence.validate_provenance(provenance, role, CANDIDATE_VERSION, source_sha, files.__getitem__)
        require(provenance["platform"] == "linux/amd64", "provenance platform mismatch")
        for path_ in WANTED[role]:
            if path_.startswith("/usr/local/bin/"):
                require(modes[path_] & 0o111 and not modes[path_] & 0o6000,
                        "final image file is not safely executable: " + path_)
                if path_ == "/usr/local/bin/cpamp-runtime-v8-entrypoint":
                    require(files[path_].startswith(b"#!/"),
                            "runtime entrypoint script lacks shebang")
                else:
                    check_amd64_elf(files[path_], path_)
        compiled_identity = check_go_binary(files[GO_BINARIES[role]], role, source_sha)
        if role == "runtime":
            require(files[RUNTIME_ENTRYPOINT] == runtime_entrypoint,
                    "Runtime entrypoint differs from locked source")
            require(hashlib.sha256(files["/usr/local/bin/cli-proxy-api"]).hexdigest() == CPA_SHA,
                    "official CPA v8.0.55 executable hash mismatch")
            manifest_file = json_object(files["/usr/local/share/cpamp/runtime/cpa-artifact.json"],
                                        "CPA artifact")
            require(manifest_file == {"schemaVersion": 1, "engine": "cpa",
                                     "version": "8.0.55", "artifactId": "sha256:" + CPA_SHA},
                    "CPA artifact identity mismatch")
            require(hashlib.sha256(files["/usr/local/share/licenses/cli-proxy-api/LICENSE"]).hexdigest()
                    == CPA_LICENSE_SHA, "official CPA license mismatch")
            require(files["/usr/local/share/cpamp/runtime/config.seed.yaml"] == seed,
                    "official v8 default seed mismatch")
        extra = {}
        if role == "manager":
            api_contract = json_object(files[API_CONTRACT], "Public API transport contract")
            require(api_contract.get("schemaVersion") == 1 and
                    api_contract.get("qualification") == API_CONTRACT_QUALIFICATION and
                    api_contract.get("apiSuffix") == "/api/v2" and
                    api_contract.get("expectedPath") == "/phase4-test/api/v2/technical",
                    "unqualified or mismatched Web/API transport contract")
            fullweb = json_object(files[WEB_MANIFEST], "Full Web file-set manifest")
            require(fullweb.get("schemaVersion") == 1 and
                    isinstance(fullweb.get("files"), list) and len(fullweb["files"]) > 0,
                    "missing complete Full Web evidence")
            names = [item.get("path") for item in fullweb["files"] if isinstance(item, dict)]
            require(len(names) == len(fullweb["files"]) and len(set(names)) == len(names) and
                    any(n == "index.html" for n in names) and
                    all(isinstance(item.get("sha256"), str) and SHA.fullmatch(item["sha256"])
                        for item in fullweb["files"]),
                    "invalid Full Web file-set evidence")
            index_html = next(item["sha256"] for item in fullweb["files"]
                              if item["path"] == "index.html")
            extra["fullWebSummary"] = {
                "manifestSHA256": digest(files[WEB_MANIFEST]),
                "indexHtmlSHA256": index_html,
                "assetCount": len(fullweb["files"]),
            }
            extra["apiTransportContract"] = {
                "sha256": digest(files[API_CONTRACT]),
                "contractId": api_contract.get("contractId"),
                "qualification": api_contract["qualification"],
                "apiSuffix": api_contract["apiSuffix"],
                "expectedPath": api_contract["expectedPath"],
            }
        return {
            "archive": path.name,
            "archiveSHA256": "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest(),
            "ociLayoutIndexDigest": digest(raw_index),
            "platformManifestDigest": manifest_id,
            "imageConfigDigest": config_id,
            "platform": "linux/amd64",
            "goBinaryIdentity": compiled_identity,
            "licenseCoverage": inventory["licenseCoverage"],
            "packagedFiles": {key: digest(value) for key, value in sorted(files.items())},
            **extra,
        }

def verify(paths, source_sha, event, root):
    require(re.fullmatch(r"[0-9a-f]{40}", source_sha) is not None, "invalid source SHA")
    require(re.fullmatch(r"[A-Za-z0-9._-]{8,128}", event) is not None, "invalid build event")
    license_bytes = (root / "LICENSE").read_bytes()
    seed = (root / "docker/runtime/config.v8.default.seed.yaml").read_bytes()
    runtime_entrypoint = (root / "docker/runtime/v8-entrypoint.sh").read_bytes()
    inventory_bytes = (root / "bin/release/embedded-third-party-inventory.txt").read_bytes()
    api_contract_bytes = (root / "tests/fixtures/r1-embedded-api-transport.json").read_bytes()
    result = {role: inspect(paths[role], role, source_sha, event,
                            license_bytes, seed, runtime_entrypoint)
              for role in ROLES}
    for role in ROLES:
        require(result[role]["packagedFiles"][image_evidence.SOURCE_INVENTORY] == digest(inventory_bytes),
                "final image source dependency inventory differs from checked source: " + role)
    require(result["manager"]["packagedFiles"][API_CONTRACT] == digest(api_contract_bytes),
            "final Manager public API transport contract differs from checked source")
    require(len({result[role]["platformManifestDigest"] for role in ROLES}) == 3,
            "three different image manifests required")
    return {
        "schemaVersion": 1,
        "status": "STRUCTURAL_CANDIDATE_EVIDENCE_ONLY_NOT_RELEASE_QUALIFIED",
        "sourceCommitClaim": source_sha,
        "candidateBuildEventClaim": event,
        "candidateVersionClaim": CANDIDATE_VERSION,
        "platform": "linux/amd64",
        "images": result,
        "licenseCoverage": {role: result[role]["licenseCoverage"] for role in ROLES},
        "licenseCoveragePolicy": {
            "originalTextsFailClosed": "Go modules and standard library, Web dependency closure, Debian base packages",
            "alpineBasePackages": image_evidence.ALPINE_POLICY,
        },
        "officialCPA": {
            "version": "8.0.55", "dockerBuildPinArchiveSHA256": CPA_ARCHIVE_SHA,
            "verifiedFinalImageExecutableSHA256": CPA_SHA,
        },
        "notVerified": [
            "trusted production producer identity or independent registry readback",
            "Full Web compiled embed and one source-paired Web/API transport fixture tested, but complete functional API compatibility",
            "independent legal acceptance of collected third-party licenses and base-package records",
            "Host, network, startup, readiness, rollback (historical 1.x contract tested separately)",
            "production release, beta, mutable aliases or channels",
        ],
    }


def parse_compose_service_images(compose_text):
    require(isinstance(compose_text, str) and compose_text.strip() != "",
            "empty rendered compose file")
    require(re.search(r":latest($|[\s'\"`@])", compose_text, re.IGNORECASE) is None,
            "rendered compose must not reference :latest")
    require(re.search(r"(?m)^\s*build\s*:", compose_text) is None,
            "rendered compose must not contain local build blocks")
    require("CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK" not in compose_text,
            "rendered compose must not define CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK")
    services = {}
    in_services = False
    current = None
    for raw in compose_text.splitlines():
        stripped = raw.strip()
        if not stripped or stripped.startswith("#"):
            continue
        indent = len(raw) - len(raw.lstrip(" "))
        if indent == 0:
            in_services = stripped == "services:"
            current = None
            continue
        if in_services and indent == 2 and stripped.endswith(":"):
            current = stripped[:-1]
            require(current not in services, "duplicate compose service: " + current)
            services[current] = None
            continue
        if in_services and current and indent == 4 and stripped.startswith("image:"):
            value = stripped.split(":", 1)[1].strip()
            if (value.startswith("'") and value.endswith("'")) or (
                    value.startswith('"') and value.endswith('"')):
                value = value[1:-1]
            require(services[current] is None, "duplicate image key in " + current)
            services[current] = value
    expected_services = {"cpamp-" + role for role in ROLES}
    require(set(services) == expected_services and all(services.values()),
            "rendered compose must define image references for cpamp-ingress, cpamp-manager and cpamp-runtime")
    return services


API_CONTRACT_ID = "cpamp-r1-embedded-public-api-transport-v1"


def verify_archive_anchor(path, role, evidence, pairing, source_commit):
    require(path.is_file(), "missing OCI archive: " + str(path))
    ev_img = evidence["images"][role]
    actual_archive_sha = digest(path.read_bytes())
    require(actual_archive_sha == ev_img["archiveSHA256"],
            "OCI archive SHA256 mismatch against evidence: " + role)
    with tarfile.open(path, mode="r:*") as archive:
        layout = json_object(read_member(archive, "oci-layout", 4096), "oci-layout")
        require(layout.get("imageLayoutVersion") == "1.0.0", "not an OCI layout")
        raw_index = read_member(archive, "index.json", 1024 * 1024)
        if "ociLayoutIndexDigest" in ev_img:
            require(digest(raw_index) == ev_img["ociLayoutIndexDigest"],
                    "OCI layout index digest mismatch against evidence: " + role)
        index = json_object(raw_index, "index")
        require(index.get("schemaVersion") == 2, "invalid OCI index schema")
        descriptors = index.get("manifests")
        require(isinstance(descriptors, list), "missing OCI index descriptors")
        matches = [m for m in descriptors if type(m) is dict and
                   type(m.get("platform")) is dict and
                   m["platform"].get("os") == "linux" and
                   m["platform"].get("architecture") == "amd64"]
        require(len(matches) == 1 and len(descriptors) == 1,
                "candidate must contain exactly one linux/amd64 manifest")
        descriptor = matches[0]
        manifest_id = descriptor.get("digest")
        require(manifest_id == ev_img["platformManifestDigest"] and
                manifest_id == pairing["images"][role]["manifest_digest"],
                "OCI archive platformManifestDigest mismatch against evidence/pairing: " + role)
        manifest = json_object(read_descriptor(archive, descriptor, (MANIFEST_TYPE,)), "manifest")
        config_descriptor = manifest.get("config")
        config = json_object(read_descriptor(archive, config_descriptor, (CONFIG_TYPE,)), "config")
        config_id = config_descriptor["digest"]
        require(config_id == ev_img["imageConfigDigest"] and
                config_id == pairing["images"][role]["image_config_digest"],
                "OCI archive imageConfigDigest mismatch against evidence/pairing: " + role)
        labels = (config.get("config") or {}).get("Labels") or {}
        require(labels.get("org.opencontainers.image.revision") == source_commit,
                "OCI archive revision label mismatch against source_commit: " + role)
        require(labels.get("io.cpamp.candidate.role") == role,
                "OCI archive role label mismatch: " + role)
        if evidence.get("candidateBuildEventClaim"):
            require(labels.get("io.cpamp.candidate.event") == evidence["candidateBuildEventClaim"],
                    "OCI archive build event label mismatch: " + role)
        layers = manifest.get("layers")
        rootfs = config.get("rootfs") or {}
        wanted = {GO_BINARIES[role], image_evidence.PROVENANCE}
        if role == "manager":
            wanted.update({WEB_MANIFEST, API_CONTRACT})
        if role == "runtime":
            wanted.update({
                "/usr/local/bin/cli-proxy-api",
                "/usr/local/share/cpamp/runtime/cpa-artifact.json",
            })
        files, _ = final_files(archive, layers, rootfs.get("diff_ids") or [], wanted)
        compiled_identity = check_go_binary(files[GO_BINARIES[role]], role, source_commit)
        require(compiled_identity.get("managerCompiledSourceClaim") ==
                ev_img["goBinaryIdentity"].get("managerCompiledSourceClaim"),
                "OCI archive Go binary source claim mismatch: " + role)
        require(digest(files[GO_BINARIES[role]]) == ev_img["packagedFiles"][GO_BINARIES[role]] and
                digest(files[GO_BINARIES[role]]) == pairing["images"][role]["binary_sha256"],
                "OCI archive binary SHA256 mismatch: " + role)
        provenance = json_object(files[image_evidence.PROVENANCE], "component provenance")
        require(provenance.get("sourceCommit") == source_commit and
                provenance.get("role") == role,
                "OCI archive provenance sourceCommit/role mismatch: " + role)
        require(digest(files[image_evidence.PROVENANCE]) == ev_img["packagedFiles"][image_evidence.PROVENANCE] and
                digest(files[image_evidence.PROVENANCE]) == pairing["images"][role]["release_provenance_sha256"],
                "OCI archive provenance SHA256 mismatch: " + role)
        if role == "manager":
            fullweb = json_object(files[WEB_MANIFEST], "Full Web file-set manifest")
            require(fullweb.get("schemaVersion") == 1 and
                    isinstance(fullweb.get("files"), list) and len(fullweb["files"]) >= 2,
                    "invalid Full Web manifest in Manager OCI archive")
            index_html = next(
                (item["sha256"] for item in fullweb["files"]
                 if isinstance(item, dict) and item.get("path") == "index.html"),
                None,
            )
            require(isinstance(index_html, str) and SHA.fullmatch(index_html) is not None,
                    "missing valid index.html entry in Manager OCI archive Full Web manifest")
            web_sha = digest(files[WEB_MANIFEST])
            require(web_sha == ev_img["packagedFiles"][WEB_MANIFEST] and
                    pairing["fullweb"] == {
                        "manifest_sha256": web_sha,
                        "index_html_sha256": index_html,
                        "asset_count": len(fullweb["files"]),
                    },
                    "OCI archive Full Web manifest or index.html summary mismatch against pairing")
            if "fullWebSummary" in ev_img:
                require(ev_img["fullWebSummary"] == {
                    "manifestSHA256": web_sha,
                    "indexHtmlSHA256": index_html,
                    "assetCount": len(fullweb["files"]),
                }, "OCI archive Full Web summary differs from manager.fullWebSummary")
            api_obj = json_object(files[API_CONTRACT], "Public API transport contract")
            api_sha = digest(files[API_CONTRACT])
            require(api_sha == ev_img["packagedFiles"][API_CONTRACT] and
                    pairing["api_contract"] == {
                        "sha256": api_sha,
                        "contract_id": api_obj.get("contractId"),
                        "qualification": api_obj.get("qualification"),
                        "api_suffix": api_obj.get("apiSuffix"),
                        "expected_path": api_obj.get("expectedPath"),
                    },
                    "OCI archive API transport contract mismatch against pairing")
        if role == "runtime":
            cpa_bytes = files["/usr/local/bin/cli-proxy-api"]
            actual_cpa_digest = digest(cpa_bytes)
            require(hashlib.sha256(cpa_bytes).hexdigest() == CPA_SHA and
                    actual_cpa_digest == ev_img["packagedFiles"].get("/usr/local/bin/cli-proxy-api") and
                    actual_cpa_digest == pairing["cpa"]["artifact_id"],
                    "OCI archive official CPA v8.0.55 executable hash mismatch")
            cpa_artifact_path = "/usr/local/share/cpamp/runtime/cpa-artifact.json"
            cpa_artifact_bytes = files[cpa_artifact_path]
            require(digest(cpa_artifact_bytes) == ev_img["packagedFiles"].get(cpa_artifact_path),
                    "OCI archive CPA artifact manifest digest mismatch against evidence")
            cpa_artifact = json_object(cpa_artifact_bytes, "CPA artifact manifest")
            require(cpa_artifact == {
                "schemaVersion": 1,
                "engine": "cpa",
                "version": "8.0.55",
                "artifactId": "sha256:" + CPA_SHA,
            }, "OCI archive CPA artifact manifest mismatch for runtime")


def verify_rendered_pairing(evidence, compose_path, pairing_path, archive_paths=None):
    require(type(evidence) is dict and evidence.get("schemaVersion") == 1,
            "invalid candidate evidence for pairing verification")
    require(compose_path.is_file(), "missing rendered compose file: " + str(compose_path))
    require(pairing_path.is_file(), "missing rendered pairing file: " + str(pairing_path))
    compose_bytes = compose_path.read_bytes()
    compose_text = compose_bytes.decode("utf-8")
    service_images = parse_compose_service_images(compose_text)
    pairing = json_object(pairing_path.read_bytes(), "embedded release pairing")
    require(pairing.get("schema_version") == 1, "invalid pairing schema_version")
    version = pairing.get("version")
    source_commit = evidence.get("sourceCommitClaim")
    image_evidence.identity(version, source_commit)
    require(version.lower() != "latest", "pairing version must not be latest")
    require(pairing.get("source_commit") == source_commit, "pairing source_commit mismatch")
    require(evidence["images"]["manager"]["goBinaryIdentity"].get("managerCompiledSourceClaim") == source_commit,
            "Manager compiled source commit mismatch")
    require(pairing.get("platform") == "linux/amd64" and evidence.get("platform") == "linux/amd64",
            "pairing platform mismatch")
    cpa_exec = evidence.get("officialCPA", {}).get("verifiedFinalImageExecutableSHA256")
    require(cpa_exec == CPA_SHA, "candidate evidence official CPA executable mismatch")
    require(pairing.get("cpa") == {"version": "8.0.55", "artifact_id": "sha256:" + CPA_SHA},
            "pairing CPA identity mismatch")
    manager_record = evidence["images"]["manager"]
    manager_files = manager_record["packagedFiles"]
    fullweb = pairing.get("fullweb")
    require(type(fullweb) is dict and
            fullweb.get("manifest_sha256") == manager_files.get(WEB_MANIFEST) and
            isinstance(fullweb.get("index_html_sha256"), str) and
            SHA.fullmatch(fullweb["index_html_sha256"]) is not None and
            type(fullweb.get("asset_count")) is int and fullweb["asset_count"] >= 2,
            "pairing fullweb summary mismatch")
    if "fullWebSummary" in manager_record:
        require(fullweb["index_html_sha256"] == manager_record["fullWebSummary"]["indexHtmlSHA256"] and
                fullweb["asset_count"] == manager_record["fullWebSummary"]["assetCount"],
                "pairing fullweb summary differs from manager.fullWebSummary")
    api_contract = pairing.get("api_contract")
    require(type(api_contract) is dict and
            api_contract.get("sha256") == manager_files.get(API_CONTRACT) and
            api_contract.get("contract_id") == API_CONTRACT_ID and
            api_contract.get("qualification") == API_CONTRACT_QUALIFICATION and
            api_contract.get("api_suffix") == "/api/v2" and
            api_contract.get("expected_path") == "/phase4-test/api/v2/technical",
            "pairing api_contract mismatch")
    compose_meta = pairing.get("compose")
    require(compose_meta == {
        "filename": "docker-compose.v2-embedded.yml",
        "sha256": digest(compose_bytes),
        "size": len(compose_bytes),
    }, "pairing compose metadata mismatch")
    images = pairing.get("images")
    require(type(images) is dict and set(images) == set(ROLES),
            "pairing images must contain manager, ingress and runtime")
    for role in ROLES:
        entry = images[role]
        require(type(entry) is dict, "invalid pairing image entry: " + role)
        ev_img = evidence["images"][role]
        require(ev_img.get("archive") == f"{role}.oci.tar",
                f"invalid or missing archive filename for {role}: {ev_img.get('archive')!r}")
        require(isinstance(ev_img.get("archiveSHA256"), str) and
                SHA.fullmatch(ev_img["archiveSHA256"]) is not None,
                f"invalid or missing archiveSHA256 for {role}")
        if "sourceCommitClaim" in ev_img and ev_img["sourceCommitClaim"] is not None:
            require(ev_img["sourceCommitClaim"] == source_commit,
                    f"{role} sourceCommitClaim differs from candidate sourceCommitClaim")
        repo = entry.get("repository")
        tag = entry.get("tag")
        manifest_digest = entry.get("manifest_digest")
        require(isinstance(repo, str) and "/" in repo and ":" not in repo and "@" not in repo,
                "invalid pairing repository: " + role)
        require(tag == version, "pairing image tag mismatch: " + role)
        require(manifest_digest == ev_img["platformManifestDigest"],
                "pairing manifest_digest mismatch: " + role)
        require(entry.get("image_config_digest") == ev_img["imageConfigDigest"],
                "pairing image_config_digest mismatch: " + role)
        require(entry.get("binary_sha256") == ev_img["packagedFiles"][GO_BINARIES[role]],
                "pairing binary_sha256 mismatch: " + role)
        require(entry.get("release_provenance_sha256") == ev_img["packagedFiles"][image_evidence.PROVENANCE],
                "pairing release_provenance_sha256 mismatch: " + role)
        expected_ref = f"{repo}:{tag}@{manifest_digest}"
        require(entry.get("image_ref") == expected_ref, "pairing image_ref mismatch: " + role)
        require(service_images["cpamp-" + role] == expected_ref,
                "rendered compose image mismatch for cpamp-" + role)
    if archive_paths is None:
        archive_paths = {role: compose_path.parent / f"{role}.oci.tar" for role in ROLES}
    require(type(archive_paths) is dict and set(archive_paths) == set(ROLES),
            "archive_paths must provide all three role OCI archives (manager, ingress, runtime)")
    for role in ROLES:
        archive = archive_paths[role]
        require(isinstance(archive, Path) and archive.is_file(),
                f"missing required OCI archive for {role}: {archive}")
        verify_archive_anchor(archive, role, evidence, pairing, source_commit)
    renderer = Path(__file__).with_name("render-embedded-compose.mjs")
    require(renderer.is_file(), "missing compose/pairing renderer: " + str(renderer))
    node_env = dict(os.environ)
    repo_node_modules = Path(__file__).resolve().parents[2] / "node_modules"
    if repo_node_modules.is_dir():
        existing = node_env.get("NODE_PATH", "")
        node_env["NODE_PATH"] = str(repo_node_modules) + (os.pathsep + existing if existing else "")
    official_cpa_sha = "e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7"
    with tempfile.TemporaryDirectory(prefix="cpamp-verify-pairing-") as temp_dir:
        ev_file = Path(temp_dir) / "candidate-evidence.json"
        ev_file.write_text(json.dumps(evidence), encoding="utf-8")
        pairing_file = Path(temp_dir) / "embedded-release-pairing.json"
        pairing_file.write_text(json.dumps(pairing), encoding="utf-8")
        cli_args = [
            "--verify",
            "--evidence", str(ev_file),
            "--compose", str(compose_path),
            "--pairing", str(pairing_file),
        ]
        for role in ROLES:
            cli_args.extend([f"--{role}-oci", str(archive_paths[role])])
        if CPA_SHA == official_cpa_sha:
            cmd = ["node", str(renderer), *cli_args]
        else:
            runner_file = Path(temp_dir) / "run-verify.mjs"
            runner_file.write_text(
                f"import {{ setExpectedCpaSha256ForTest, runCli }} from {json.dumps(renderer.resolve().as_uri())};\n"
                f"setExpectedCpaSha256ForTest({json.dumps(CPA_SHA)});\n"
                "try {\n"
                "  runCli(process.argv.slice(2));\n"
                "} catch (err) {\n"
                "  console.error('STOP: ' + err.message);\n"
                "  process.exit(1);\n"
                "}\n",
                encoding="utf-8",
            )
            cmd = ["node", str(runner_file), *cli_args]
        proc = subprocess.run(
            cmd, env=node_env, capture_output=True, text=True, timeout=20, check=False,
        )
    require(proc.returncode == 0,
            "compose/pairing topology verification failed: " +
            (proc.stderr.strip() or proc.stdout.strip()))
    return pairing


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manager", type=Path, help="OCI archive from same local build event")
    parser.add_argument("--ingress", type=Path)
    parser.add_argument("--runtime", type=Path)
    parser.add_argument("--source-commit")
    parser.add_argument("--build-event")
    parser.add_argument("--source-root", type=Path, default=Path("."))
    parser.add_argument("--out", type=Path)
    parser.add_argument("--evidence", type=Path, help="Existing candidate-evidence.json for rendered pairing check")
    parser.add_argument("--verify-compose", type=Path, help="Rendered docker-compose.v2-embedded.yml to verify")
    parser.add_argument("--verify-pairing", type=Path, help="Rendered embedded-release-pairing.json to verify")
    args = parser.parse_args()
    if args.evidence and not args.out:
        require(args.verify_compose is not None and args.verify_pairing is not None,
                "--evidence mode requires --verify-compose and --verify-pairing")
        evidence = json_object(args.evidence.read_bytes(), "candidate evidence")
        archives = {
            role: getattr(args, role) or (args.evidence.parent / f"{role}.oci.tar")
            for role in ROLES
        }
        verify_rendered_pairing(evidence, args.verify_compose, args.verify_pairing, archives)
        print("VERIFIED RENDERED COMPOSE AND PAIRING AGAINST CANDIDATE EVIDENCE: " + str(args.verify_pairing))
        return
    require(all(getattr(args, role) is not None for role in ROLES) and
            args.source_commit and args.build_event and args.out is not None,
            "--manager, --ingress, --runtime, --source-commit, --build-event and --out are required")
    evidence = verify({role: getattr(args, role) for role in ROLES},
                      args.source_commit, args.build_event, args.source_root)
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(evidence, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    if args.verify_compose is not None or args.verify_pairing is not None:
        require(args.verify_compose is not None and args.verify_pairing is not None,
                "both --verify-compose and --verify-pairing must be supplied together")
        verify_rendered_pairing(evidence, args.verify_compose, args.verify_pairing,
                                {role: getattr(args, role) for role in ROLES})
    print("STRUCTURAL CANDIDATE EVIDENCE ONLY; Release/Registry/Channel STOP: " + str(args.out))

if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, tarfile.TarError, json.JSONDecodeError, KeyError) as exc:
        print("STOP: invalid Embedded candidate: " + str(exc), file=sys.stderr)
        sys.exit(1)
