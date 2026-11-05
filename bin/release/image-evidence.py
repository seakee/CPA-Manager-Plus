#!/usr/bin/env python3
"""Build and validate deterministic, byte-bound image provenance and notices.

These are unsigned source claims, not protected-producer attestations or legal approval.
"""
import argparse
import hashlib
import json
import re
from pathlib import Path, PurePosixPath

SOURCE = "https://github.com/seakee/CPA-Manager-Plus"
CPA_SOURCE = "https://github.com/router-for-me/CLIProxyAPI"
LICENSE_ROOT = "/usr/local/share/licenses/"
INVENTORY = "/usr/local/share/cpamp/release/third-party-inventory.txt"
PROVENANCE = "/usr/local/share/cpamp/provenance.json"
SOURCE_INVENTORY = "/usr/local/share/cpamp/release/source-dependency-inventory.json"
COMPONENTS = {"manager": ("manager", "web", "base-image"),
              "ingress": ("ingress", "base-image"),
              "runtime": ("runtime-supervisor", "base-image")}
BINARIES = {"manager": (("manager", "cpa-manager-plus"),),
            "ingress": (("ingress", "cpamp-ingress"),),
            "runtime": (("runtime-supervisor", "cpamp-runtime-supervisor"), ("cpa", "cli-proxy-api"))}
MODULES = {"manager": "apps/manager-server", "ingress": "apps/ingress",
           "runtime-supervisor": "apps/runtime-supervisor"}
SHA = re.compile(r"^sha256:[0-9a-f]{64}$")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def digest(data):
    return "sha256:" + hashlib.sha256(data).hexdigest()


def encoded(value):
    return (json.dumps(value, indent=2, sort_keys=True, ensure_ascii=False) + "\n").encode()


def identity(version, commit):
    require(isinstance(version, str) and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._+-]{0,127}", version)
            and version.lower() != "unknown", "VERSION must be non-empty and not unknown")
    require(isinstance(commit, str) and re.fullmatch(r"[0-9a-f]{40}", commit),
            "SOURCE_COMMIT must be an exact non-unknown 40-hex commit")


def notice_path(component, relative):
    require(isinstance(relative, str) and relative and not relative.startswith("/") and
            ".." not in PurePosixPath(relative).parts and "\\" not in relative,
            "unsafe license inventory path")
    return LICENSE_ROOT + component + "/" + relative


def validate_inventory(value, role, read_file, expected_source=None):
    require(type(value) is dict and value.get("schemaVersion") == 1 and
            value.get("status") == "VERIFIED_LICENSE_COLLECTION", "unverified third-party inventory")
    require(value.get("role") == role and type(value.get("components")) is dict and
            set(value["components"]) == set(COMPONENTS[role]), "incomplete component license inventory")
    source_bytes = read_file(SOURCE_INVENTORY)
    require(value.get("sourceInventorySHA256") == digest(source_bytes), "source dependency inventory hash mismatch")
    source = json.loads(source_bytes)
    require(source.get("schemaVersion") == 1 and source.get("status") == "SOURCE_MANIFESTS_VERIFIED" and
            type(source.get("inputs")) is dict, "invalid source dependency inventory")
    if expected_source is not None:
        require(source_bytes == expected_source, "source dependency inventory differs from checked source")
    for component, entry in value["components"].items():
        raw = read_file(notice_path(component, "inventory.json"))
        require(digest(raw) == entry.get("inventorySHA256"), "component inventory hash mismatch: " + component)
        inventory = json.loads(raw)
        require(inventory == entry.get("inventory") and inventory.get("schemaVersion") == 1 and
                inventory.get("component") == component and isinstance(inventory.get("dependencies"), list)
                and inventory["dependencies"], "empty or invalid license component: " + component)
        if component in MODULES:
            for name in ("go.mod", "go.sum"):
                key = MODULES[component] + "/" + name
                if key in source["inputs"]:
                    require(inventory.get("inputs", {}).get(name) == source["inputs"][key],
                            "Go license inputs differ from source: " + component)
        elif component == "web":
            require(inventory.get("inputs") == {name: source["inputs"][name]
                    for name in ("apps/web/package.json", "package-lock.json")},
                    "Web license inputs differ from source")
        identities = set()
        for dep in inventory["dependencies"]:
            require(isinstance(dep.get("name"), str) and dep["name"] and
                    isinstance(dep.get("version"), str) and dep["version"], "missing dependency identity")
            key = dep["name"], dep["version"]
            require(key not in identities, "duplicate dependency identity")
            identities.add(key)
            require(isinstance(dep.get("notices"), list), "missing dependency notices")
            if component == "base-image" and inventory.get("ecosystem") == "alpine":
                require(dep.get("licenseExpression") and dep.get("evidence") == "installed-apk-database",
                        "missing installed Alpine license record")
            else:
                require(dep["notices"], "missing original dependency license/NOTICE")
            for notice in dep["notices"]:
                path = notice_path(component, notice.get("path"))
                require(SHA.fullmatch(notice.get("sha256", "")) and
                        digest(read_file(path)) == notice["sha256"], "license text hash mismatch: " + path)
        if component in MODULES:
            expected = {(item["name"], item["version"]) for item in source["goModules"][component]}
            require(expected <= identities, "incomplete declared Go module coverage: " + component)
            require(any(name == "Go standard library and bundled sources" for name, _ in identities),
                    "missing Go standard-library license")
        elif component == "web":
            require(set(source["webDependencies"]) <= {name for name, _ in identities},
                    "incomplete direct Web dependency coverage")
    require(read_file(LICENSE_ROOT + "cpamp/LICENSE"), "missing CPAMP LICENSE")
    if role == "runtime":
        require(read_file(LICENSE_ROOT + "cli-proxy-api/LICENSE"), "missing CPA LICENSE")


def validate_provenance(value, role, version, commit, read_file):
    identity(version, commit)
    require(type(value) is dict and type(value.get("schemaVersion")) is int and value["schemaVersion"] == 1,
            "invalid provenance schemaVersion")
    require(value.get("role") == role and value.get("version") == version and
            value.get("sourceCommit") == commit and value.get("source") == SOURCE,
            "provenance source/version identity mismatch")
    require(value.get("platform") in ("linux/amd64", "linux/arm64"), "invalid provenance platform")
    require(value.get("licenseInventorySHA256") == digest(read_file(INVENTORY)), "provenance inventory hash mismatch")
    components = value.get("components")
    expected = [name for name, _ in BINARIES[role]] + (["web"] if role == "manager" else [])
    require(isinstance(components, list) and [item.get("name") for item in components] == expected,
            "incomplete provenance components")
    paths = {name: "/usr/local/bin/" + binary for name, binary in BINARIES[role]}
    paths["web"] = "/usr/local/share/cpamp/release/fullweb-manifest.json"
    for item in components:
        require(item.get("path") == paths[item["name"]], "provenance component path mismatch")
        require(isinstance(item.get("path"), str) and SHA.fullmatch(item.get("sha256", "")) and
                digest(read_file(item["path"])) == item["sha256"], "provenance component hash mismatch")
        if item["name"] == "cpa":
            artifact = json.loads(read_file("/usr/local/share/cpamp/runtime/cpa-artifact.json"))
            require(artifact.get("schemaVersion") == 1 and artifact.get("engine") == "cpa" and
                    artifact.get("version") == item.get("version") and artifact.get("artifactId") == item["sha256"]
                    and item.get("source") == CPA_SOURCE and item.get("release") == CPA_SOURCE + "/releases/tag/v" + item["version"],
                    "CPA provenance differs from verified artifact")
        else:
            require(item.get("source") == SOURCE and item.get("sourceCommit") == commit and
                    item.get("version") == version, "component source claim mismatch")
        if item["name"] in MODULES:
            require(item.get("module") == "github.com/seakee/cpa-manager-plus/" + MODULES[item["name"]],
                    "component Go module identity mismatch")


def build(root, role, version, commit, platform, source_inventory):
    identity(version, commit)
    def read_file(path):
        return (root / path.lstrip("/")).read_bytes()
    components = {}
    for name in COMPONENTS[role]:
        raw = read_file(notice_path(name, "inventory.json"))
        components[name] = {"inventorySHA256": digest(raw), "inventory": json.loads(raw)}
    source_path = root / SOURCE_INVENTORY.lstrip("/")
    source_path.parent.mkdir(parents=True, exist_ok=True)
    source_path.write_bytes(source_inventory.read_bytes())
    inventory = {"schemaVersion": 1, "status": "VERIFIED_LICENSE_COLLECTION", "role": role,
                 "scope": "Original Go/Web notices; installed base-package license records; not legal approval",
                 "sourceInventorySHA256": digest(source_path.read_bytes()), "components": components}
    validate_inventory(inventory, role, read_file)
    (root / INVENTORY.lstrip("/")).write_bytes(encoded(inventory))
    items = []
    for name, binary in BINARIES[role]:
        path = "/usr/local/bin/" + binary
        item = {"name": name, "path": path, "sha256": digest(read_file(path))}
        if name == "cpa":
            artifact = json.loads(read_file("/usr/local/share/cpamp/runtime/cpa-artifact.json"))
            item.update(version=artifact["version"], source=CPA_SOURCE,
                        release=CPA_SOURCE + "/releases/tag/v" + artifact["version"])
        else:
            item.update(version=version, source=SOURCE, sourceCommit=commit,
                        module="github.com/seakee/cpa-manager-plus/" + MODULES[name])
        items.append(item)
    if role == "manager":
        path = "/usr/local/share/cpamp/release/fullweb-manifest.json"
        items.append({"name": "web", "path": path, "sha256": digest(read_file(path)),
                      "version": version, "source": SOURCE, "sourceCommit": commit})
    provenance = {"schemaVersion": 1, "role": role, "version": version, "sourceCommit": commit,
                  "source": SOURCE, "platform": platform, "components": items,
                  "licenseInventorySHA256": digest(read_file(INVENTORY))}
    validate_provenance(provenance, role, version, commit, read_file)
    (root / PROVENANCE.lstrip("/")).write_bytes(encoded(provenance))
    return provenance


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--role", choices=tuple(COMPONENTS), required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--source-commit", required=True)
    parser.add_argument("--platform", required=True)
    parser.add_argument("--source-inventory", type=Path, required=True)
    args = parser.parse_args()
    build(args.root, args.role, args.version, args.source_commit, args.platform, args.source_inventory)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, KeyError, TypeError) as exc:
        raise SystemExit("STOP: invalid image evidence: " + str(exc)) from exc
