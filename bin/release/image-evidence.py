#!/usr/bin/env python3
"""Build and validate deterministic, byte-bound provenance and notices.

The same contract covers OCI images and the Native package; only the layout
roots differ. These are unsigned source claims, not protected-producer
attestations or legal approval.
"""
import argparse
import hashlib
import json
import re
from pathlib import Path, PurePosixPath

SOURCE = "https://github.com/seakee/CPA-Manager-Plus"
CPA_SOURCE = "https://github.com/router-for-me/CLIProxyAPI"
# An image carries these paths inside the container root; the Native tarball
# carries the same relative layout under its package root.
LAYOUTS = {
    "image": {
        "licenseRoot": "/usr/local/share/licenses/",
        "inventory": "/usr/local/share/cpamp/release/third-party-inventory.txt",
        "provenance": "/usr/local/share/cpamp/provenance.json",
        "sourceInventory": "/usr/local/share/cpamp/release/source-dependency-inventory.json",
        "binRoot": "/usr/local/bin/",
        "artifact": "/usr/local/share/cpamp/runtime/cpa-artifact.json",
        "webManifest": "/usr/local/share/cpamp/release/fullweb-manifest.json",
        "platforms": ("linux/amd64", "linux/arm64"),
    },
    "native": {
        "licenseRoot": "share/licenses/",
        "inventory": "share/cpamp/release/third-party-inventory.txt",
        "provenance": "share/cpamp/provenance.json",
        "sourceInventory": "share/cpamp/release/source-dependency-inventory.json",
        "binRoot": "bin/",
        "artifact": "share/cpamp/runtime/cpa-artifact.json",
        "webManifest": "share/cpamp/release/fullweb-manifest.json",
        # The Native package ships linux/amd64 only; other platforms fall back
        # to 1.x Stable and must never be relabelled with this evidence.
        "platforms": ("linux/amd64",),
    },
}
LICENSE_ROOT = LAYOUTS["image"]["licenseRoot"]
INVENTORY = LAYOUTS["image"]["inventory"]
PROVENANCE = LAYOUTS["image"]["provenance"]
SOURCE_INVENTORY = LAYOUTS["image"]["sourceInventory"]
COMPONENTS = {"manager": ("manager", "web", "base-image"),
              "ingress": ("ingress", "base-image"),
              "runtime": ("runtime-supervisor", "base-image"),
              # A tarball cannot own the host OS, so it has no base-image
              # component: it ships only the licenses of the Go modules and
              # the embedded Web closure it actually links.
              "native": ("manager", "web", "ingress", "runtime-supervisor")}
BINARIES = {"manager": (("manager", "cpa-manager-plus"),),
            "ingress": (("ingress", "cpamp-ingress"),),
            "runtime": (("runtime-supervisor", "cpamp-runtime-supervisor"), ("cpa", "cli-proxy-api")),
            "native": (("manager", "cpa-manager-plus"),
                       ("ingress", "cpa-manager-plus-ingress"),
                       ("runtime-supervisor", "cpa-manager-plus-runtime-supervisor"),
                       ("cpa", "cli-proxy-api"))}
PROVENANCE_COMPONENTS = {"manager": ("manager", "web"),
                         "ingress": ("ingress",),
                         "runtime": ("runtime-supervisor", "cpa"),
                         "native": ("manager", "ingress", "runtime-supervisor", "cpa", "web")}
ROLE_TARGET = {role: ("native" if role == "native" else "image") for role in COMPONENTS}
MODULES = {"manager": "apps/manager-server", "ingress": "apps/ingress",
           "runtime-supervisor": "apps/runtime-supervisor"}
SHA = re.compile(r"^sha256:[0-9a-f]{64}$")
VERIFIED_STATUS = "VERIFIED_LICENSE_COLLECTION"
# A v2 Native package built with a CPA artifact that is not the Dockerfile pin
# may still exist for local verification, but it is never release evidence: it
# must not claim the official release or a verified status.
UNPINNED_STATUS = "UNPINNED_LOCAL_CPA"
UNPINNED_CPA_SOURCE = "local-unpinned-cpa"
# Go, Web and Debian base-image components must carry original license or
# copyright texts, so a missing original fails the build. Alpine's installed
# APK database only promises an SPDX expression per package; its evidence
# states whether packaged originals were also available.
ALPINE_POLICY = "apk-license-metadata-and-available-packaged-notices"
ALPINE_METADATA_ONLY = "installed-apk-database-metadata-only"
ALPINE_PACKAGED = "installed-apk-database-and-packaged-notices"
ORIGINAL_TEXT_POLICY = "original-license-texts-fail-closed"
# The base OS of each role is fixed by its Dockerfile. Binding it here keeps a
# runtime image from being relabelled as Alpine database-metadata coverage.
BASE_ECOSYSTEMS = {"manager": "alpine", "ingress": "alpine", "runtime": "debian"}


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


def layout(role):
    return LAYOUTS[ROLE_TARGET[role]]


def notice_path(component, relative, target="image"):
    require(target in LAYOUTS, "unknown evidence layout: " + str(target))
    require(isinstance(relative, str) and relative and not relative.startswith("/") and
            ".." not in PurePosixPath(relative).parts and "\\" not in relative,
            "unsafe license inventory path")
    return LAYOUTS[target]["licenseRoot"] + component + "/" + relative


def alpine_coverage(dependencies):
    with_notices = sum(1 for dep in dependencies if dep["notices"])
    return {"packages": len(dependencies), "metadataOnlyPackages": len(dependencies) - with_notices,
            "packagedNoticePackages": with_notices}


def license_coverage(components):
    # The aggregate inventory and provenance must state, per component, whether
    # full original texts are enforced or only the Alpine metadata boundary.
    result = {}
    for name, entry in components.items():
        inventory = entry["inventory"]
        dependencies = inventory["dependencies"]
        if name == "base-image" and inventory.get("ecosystem") == "alpine":
            result[name] = {"policy": ALPINE_POLICY, "originalTexts": "available-packaged-notices-only",
                            **alpine_coverage(dependencies)}
        elif name == "base-image":
            result[name] = {"policy": "debian-copyright-files-fail-closed",
                            "originalTexts": "complete-for-installed-packages",
                            "packages": len(dependencies)}
        else:
            result[name] = {"policy": ORIGINAL_TEXT_POLICY,
                            "originalTexts": "complete-for-installed-dependencies",
                            "packages": len(dependencies)}
    return result


def validate_inventory(value, role, read_file, expected_source=None, allow_unpinned=False):
    lay = layout(role)
    target = ROLE_TARGET[role]
    allowed_status = (VERIFIED_STATUS, UNPINNED_STATUS) if allow_unpinned else (VERIFIED_STATUS,)
    require(type(value) is dict and value.get("schemaVersion") == 1 and
            value.get("status") in allowed_status, "unverified third-party inventory")
    require(value.get("role") == role and type(value.get("components")) is dict and
            set(value["components"]) == set(COMPONENTS[role]), "incomplete component license inventory")
    source_bytes = read_file(lay["sourceInventory"])
    require(value.get("sourceInventorySHA256") == digest(source_bytes), "source dependency inventory hash mismatch")
    source = json.loads(source_bytes)
    require(source.get("schemaVersion") == 1 and source.get("status") == "SOURCE_MANIFESTS_VERIFIED" and
            type(source.get("inputs")) is dict, "invalid source dependency inventory")
    if expected_source is not None:
        require(source_bytes == expected_source, "source dependency inventory differs from checked source")
    for component, entry in value["components"].items():
        raw = read_file(notice_path(component, "inventory.json", target))
        require(digest(raw) == entry.get("inventorySHA256"), "component inventory hash mismatch: " + component)
        inventory = json.loads(raw)
        require(inventory == entry.get("inventory") and inventory.get("schemaVersion") == 1 and
                inventory.get("component") == component and isinstance(inventory.get("dependencies"), list)
                and inventory["dependencies"], "empty or invalid license component: " + component)
        if component == "base-image":
            require(inventory.get("ecosystem") == BASE_ECOSYSTEMS[role],
                    "base-image ecosystem differs from the image role: " + component)
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
        notice_paths = set()
        for dep in inventory["dependencies"]:
            require(isinstance(dep.get("name"), str) and dep["name"] and
                    isinstance(dep.get("version"), str) and dep["version"], "missing dependency identity")
            key = dep["name"], dep["version"]
            require(key not in identities, "duplicate dependency identity")
            identities.add(key)
            require(isinstance(dep.get("notices"), list), "missing dependency notices")
            if component == "base-image" and inventory.get("ecosystem") == "alpine":
                require(isinstance(dep.get("licenseExpression"), str) and dep["licenseExpression"].strip(),
                        "missing installed Alpine license expression: " + dep["name"])
                require(dep.get("evidence") == (ALPINE_PACKAGED if dep["notices"] else ALPINE_METADATA_ONLY),
                        "invalid installed Alpine license evidence: " + dep["name"])
                owner = (PurePosixPath("packaged-notices") / dep["name"]).parts
                for notice in dep["notices"]:
                    parts = PurePosixPath(str(notice.get("path", ""))).parts
                    require(len(parts) > len(owner) and parts[:len(owner)] == owner,
                            "Alpine notice path does not belong to its package: " + dep["name"])
            else:
                require(dep["notices"], "missing original dependency license/NOTICE")
            for notice in dep["notices"]:
                path = notice_path(component, notice.get("path"), target)
                require(path not in notice_paths, "notice file claimed by multiple dependencies: " + path)
                notice_paths.add(path)
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
        if component == "base-image" and inventory.get("ecosystem") == "alpine":
            require(inventory.get("coveragePolicy") == ALPINE_POLICY and
                    inventory.get("coverage") == alpine_coverage(inventory["dependencies"]),
                    "Alpine license coverage summary mismatch: " + component)
    require(value.get("licenseCoverage") == license_coverage(value["components"]),
            "license coverage summary differs from component inventories")
    require(read_file(lay["licenseRoot"] + "cpamp/LICENSE"), "missing CPAMP LICENSE")
    if any(name == "cpa" for name, _ in BINARIES[role]):
        require(read_file(lay["licenseRoot"] + "cli-proxy-api/LICENSE"), "missing CPA LICENSE")


def validate_provenance(value, role, version, commit, read_file, allow_unpinned=False):
    lay = layout(role)
    identity(version, commit)
    require(type(value) is dict and type(value.get("schemaVersion")) is int and value["schemaVersion"] == 1,
            "invalid provenance schemaVersion")
    require(value.get("role") == role and value.get("version") == version and
            value.get("sourceCommit") == commit and value.get("source") == SOURCE,
            "provenance source/version identity mismatch")
    if role == "native":
        # Release evidence is verified or explicitly local; the release
        # validator never accepts the local marker.
        expected_status = UNPINNED_STATUS if allow_unpinned else VERIFIED_STATUS
        require(value.get("status") == expected_status, "unverified provenance status")
    require(value.get("platform") in lay["platforms"], "invalid provenance platform")
    aggregate = read_file(lay["inventory"])
    require(value.get("licenseInventorySHA256") == digest(aggregate), "provenance inventory hash mismatch")
    coverage = json.loads(aggregate).get("licenseCoverage")
    require(isinstance(coverage, dict) and coverage and value.get("licenseCoverage") == coverage,
            "provenance license coverage differs from aggregate inventory")
    components = value.get("components")
    expected = list(PROVENANCE_COMPONENTS[role])
    require(isinstance(components, list) and [item.get("name") for item in components] == expected,
            "incomplete provenance components")
    paths = {name: lay["binRoot"] + binary for name, binary in BINARIES[role]}
    paths["web"] = lay["webManifest"]
    for item in components:
        require(item.get("path") == paths[item["name"]], "provenance component path mismatch")
        require(isinstance(item.get("path"), str) and SHA.fullmatch(item.get("sha256", "")) and
                digest(read_file(item["path"])) == item["sha256"], "provenance component hash mismatch")
        if item["name"] == "cpa":
            artifact = json.loads(read_file(lay["artifact"]))
            require(artifact.get("schemaVersion") == 1 and artifact.get("engine") == "cpa" and
                    artifact.get("version") == item.get("version") and artifact.get("artifactId") == item["sha256"],
                    "CPA provenance differs from verified artifact")
            if allow_unpinned:
                require(item.get("source") == UNPINNED_CPA_SOURCE and "release" not in item,
                        "unpinned CPA provenance must not claim an official release")
            else:
                require(item.get("source") == CPA_SOURCE and
                        item.get("release") == CPA_SOURCE + "/releases/tag/v" + item["version"],
                        "CPA provenance differs from verified artifact")
        else:
            require(item.get("source") == SOURCE and item.get("sourceCommit") == commit and
                    item.get("version") == version, "component source claim mismatch")
        if item["name"] in MODULES:
            require(item.get("module") == "github.com/seakee/cpa-manager-plus/" + MODULES[item["name"]],
                    "component Go module identity mismatch")


def build(root, role, version, commit, platform, source_inventory, cpa_official=True):
    lay = layout(role)
    target = ROLE_TARGET[role]
    identity(version, commit)
    if not cpa_official and role != "native":
        raise ValueError("only the Native package can carry unpinned CPA evidence")
    status = VERIFIED_STATUS if cpa_official else UNPINNED_STATUS
    def read_file(path):
        return (root / path.lstrip("/")).read_bytes()
    components = {}
    for name in COMPONENTS[role]:
        raw = read_file(notice_path(name, "inventory.json", target))
        components[name] = {"inventorySHA256": digest(raw), "inventory": json.loads(raw)}
    source_path = root / lay["sourceInventory"].lstrip("/")
    source_path.parent.mkdir(parents=True, exist_ok=True)
    source_path.write_bytes(source_inventory.read_bytes())
    inventory = {"schemaVersion": 1, "status": status, "role": role,
                 "scope": ("Original Go/Web notices and Debian copyright files fail closed; Alpine base "
                           "packages record APK SPDX expressions plus available packaged originals; "
                           "not legal approval"),
                 "licenseCoverage": license_coverage(components),
                 "sourceInventorySHA256": digest(source_path.read_bytes()), "components": components}
    validate_inventory(inventory, role, read_file, allow_unpinned=not cpa_official)
    (root / lay["inventory"].lstrip("/")).write_bytes(encoded(inventory))
    items = []
    for name, binary in BINARIES[role]:
        path = lay["binRoot"] + binary
        item = {"name": name, "path": path, "sha256": digest(read_file(path))}
        if name == "cpa":
            artifact = json.loads(read_file(lay["artifact"]))
            if cpa_official:
                item.update(version=artifact["version"], source=CPA_SOURCE,
                            release=CPA_SOURCE + "/releases/tag/v" + artifact["version"])
            else:
                # Local build: never claim the official release.
                item.update(version=artifact["version"], source=UNPINNED_CPA_SOURCE)
        else:
            item.update(version=version, source=SOURCE, sourceCommit=commit,
                        module="github.com/seakee/cpa-manager-plus/" + MODULES[name])
        items.append(item)
    if "web" in PROVENANCE_COMPONENTS[role]:
        path = lay["webManifest"]
        items.append({"name": "web", "path": path, "sha256": digest(read_file(path)),
                      "version": version, "source": SOURCE, "sourceCommit": commit})
    provenance = {"schemaVersion": 1, "role": role, "version": version, "sourceCommit": commit,
                  "source": SOURCE, "platform": platform, "components": items,
                  "licenseCoverage": inventory["licenseCoverage"],
                  "licenseInventorySHA256": digest(read_file(lay["inventory"]))}
    if role == "native":
        provenance["status"] = status
    validate_provenance(provenance, role, version, commit, read_file, allow_unpinned=not cpa_official)
    (root / lay["provenance"].lstrip("/")).write_bytes(encoded(provenance))
    return provenance


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--role", choices=tuple(COMPONENTS), required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--source-commit", required=True)
    parser.add_argument("--platform", required=True)
    parser.add_argument("--source-inventory", type=Path, required=True)
    parser.add_argument("--cpa-unpinned", action="store_true",
                        help="Native only: label evidence as local, never as the official CPA release")
    args = parser.parse_args()
    try:
        if args.cpa_unpinned and args.role != "native":
            raise ValueError("--cpa-unpinned is only valid for the Native role")
        build(args.root, args.role, args.version, args.source_commit, args.platform,
              args.source_inventory, cpa_official=not args.cpa_unpinned)
    except (ValueError, OSError, KeyError, TypeError) as exc:
        # Keep the published image diagnostic prefix stable; only the Native
        # package uses the generalized wording.
        label = ("invalid image evidence" if ROLE_TARGET[args.role] == "image"
                 else "invalid provenance evidence")
        raise SystemExit("STOP: " + label + ": " + str(exc)) from exc


if __name__ == "__main__":
    main()
