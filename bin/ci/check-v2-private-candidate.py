#!/usr/bin/env python3
"""Read-only inventory of three separately built local CPAMP v2 Linux/amd64 or Linux/arm64 images.

Only creates and removes stopped audit containers. It does not start containers or execute image entrypoints,
mount user volumes, publish ports, contact a registry, or authorize a release.
This is *not* an immutable deployment manifest or a Full Web/NOTICE attestation.
"""

import argparse
import hashlib
import json
import re
import subprocess
import sys
import tempfile
import uuid
from pathlib import Path

CPA_EXECUTABLE_SHA256 = {
    "amd64": "e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7",
    "arm64": "4a5b43cc3700d36c72b7c1953e9b11eca0888c13b789ea2f99471117504a7d5d",
}
CPA_VERSION = "8.0.55"
CPA_LICENSE_PATH = "/usr/local/share/licenses/cli-proxy-api/LICENSE"
# Original LICENSE bytes from the checksum-verified official v8.0.55 archive.
CPA_LICENSE_SHA256 = "879792e89cf1bdd6a8d446033ec87e30496f97dcafc4656dc53f641509b346a6"
EXTRACT = {
    "manager": ("/usr/local/bin/cpa-manager-plus",),
    "ingress": ("/usr/local/bin/cpamp-ingress",),
    "runtime": (
        "/usr/local/bin/cpamp-runtime-supervisor",
        "/usr/local/bin/cli-proxy-api",
        "/usr/local/share/cpamp/runtime/cpa-artifact.json",
        CPA_LICENSE_PATH,
    ),
}


def command(*args):
    result = subprocess.run(args, check=True, capture_output=True, text=True)
    return result.stdout.strip()


def inspect_image(ref):
    entries = json.loads(command("docker", "image", "inspect", ref))
    if len(entries) != 1:
        raise ValueError(f"expected one image for {ref}")
    item = entries[0]
    if item.get("Os") != "linux" or item.get("Architecture") not in CPA_EXECUTABLE_SHA256:
        raise ValueError(f"unexpected image platform: {ref}")
    if (item.get("Config") or {}).get("Volumes"):
        raise ValueError(f"image declares volumes; do not allocate anonymous audit volumes: {ref}")
    image_id = item.get("Id", "")
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", image_id):
        raise ValueError(f"invalid image config ID: {ref}")
    return {"inputRef": ref, "imageConfigId": image_id,
            "architecture": item["Architecture"],
            "repoDigestsObserved": sorted(item.get("RepoDigests") or [])}


def extract_file(container, path, destination):
    command("docker", "cp", f"{container}:{path}", str(destination))
    if destination.is_symlink() or not destination.is_file():
        raise ValueError(f"missing regular file {path}")
    return hashlib.sha256(destination.read_bytes()).hexdigest()


def check_artifact(content, architecture):
    value = json.loads(content)
    if (type(value) is not dict or type(value.get("schemaVersion")) is not int or
            value.get("schemaVersion") != 1 or
            value.get("engine") != "cpa" or value.get("version") != CPA_VERSION or
            value.get("artifactId") != "sha256:" + CPA_EXECUTABLE_SHA256[architecture]):
        raise ValueError("CPA artifact manifest does not identify pinned official v8.0.55")
    return value


def check_cpa_license(path):
    if path.is_symlink() or not path.is_file():
        raise ValueError("missing regular CPA LICENSE")
    if path.stat().st_mode & 0o222:
        raise ValueError("packaged CPA LICENSE must be read-only")
    if hashlib.sha256(path.read_bytes()).hexdigest() != CPA_LICENSE_SHA256:
        raise ValueError("packaged CPA LICENSE differs from official v8.0.55")


def audit_one(role, image, tmpdir):
    record = inspect_image(image)
    name = "cpamp-v2-readonly-" + uuid.uuid4().hex[:16]
    created = False
    try:
        # No docker start: create a stopped container to read packaged bytes.
        command("docker", "create", "--pull=never", "--platform", "linux/" + record["architecture"],
                "--name", name, "--entrypoint", "/bin/true", record["imageConfigId"])
        created = True
        files = {}
        for i, path in enumerate(EXTRACT[role]):
            destination = tmpdir / f"{role}-{i}"
            digest = extract_file(name, path, destination)
            files[path] = digest
            if path.endswith("cpa-artifact.json"):
                check_artifact(destination.read_text(encoding="utf-8"), record["architecture"])
            if path == CPA_LICENSE_PATH:
                check_cpa_license(destination)
        record["files"] = files
        if role == "runtime" and files["/usr/local/bin/cli-proxy-api"] != CPA_EXECUTABLE_SHA256[record["architecture"]]:
            raise ValueError("actual bundled CPA executable differs from pinned v8.0.55")
        return record
    finally:
        if created:
            command("docker", "rm", "-f", "-v", name)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-commit", required=True, help="exact checked-out product SHA (recorded, not attested)")
    parser.add_argument("--manager-image", required=True)
    parser.add_argument("--ingress-image", required=True)
    parser.add_argument("--runtime-image", required=True)
    parser.add_argument("--output", required=True, help="local JSON evidence output file")
    args = parser.parse_args()
    if not re.fullmatch(r"[0-9a-f]{40}", args.source_commit):
        parser.error("--source-commit must be an exact 40-hex SHA")
    with tempfile.TemporaryDirectory(prefix="cpamp-v2-candidate-") as temp:
        temporary = Path(temp)
        images = {
            role: audit_one(role, image, temporary)
            for role, image in (("manager", args.manager_image),
                                ("ingress", args.ingress_image),
                                ("runtime", args.runtime_image))
        }
    architectures = {item["architecture"] for item in images.values()}
    if len(architectures) != 1:
        raise ValueError("Manager, Ingress and Runtime images must use one matching architecture")
    architecture = architectures.pop()
    evidence = {
        "schemaVersion": 1,
        "result": "PARTIAL_BINARY_IDENTITY_ONLY_NOT_RELEASE_QUALIFIED",
        "sourceCommitClaim": args.source_commit,
        "platform": "linux/" + architecture,
        "pinnedCPAExecutableSHA256": CPA_EXECUTABLE_SHA256[architecture],
        "images": images,
        "notVerified": [
            "source-to-image build provenance or SourceCommit labels",
            "image manifest/index digest and immutable shared deployment set",
            "full-Web complete asset-set hash and Manager embedded API pairing",
            "Docker running-network topology, live-key authentication, readiness, rollback",
            "CPAMP and other third-party license/NOTICE completeness or legal acceptance",
            "registry availability, installer, supported paths, public Beta or release",
        ],
    }
    dest = Path(args.output)
    dest.parent.mkdir(parents=True, exist_ok=True)
    dest.write_text(json.dumps(evidence, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"PARTIAL only: local image and CPA binary identity saved to {dest}; release remains STOP")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError, json.JSONDecodeError) as exc:
        print(f"STOP: inventory incomplete or source mismatched: {exc}", file=sys.stderr)
        sys.exit(1)
