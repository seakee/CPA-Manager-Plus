#!/usr/bin/env python3
"""Collect original dependency notices and exact installed-package records, offline.

Run after go mod download / npm ci, inside the corresponding build stage.
No network requests, timestamps, host paths, or inferred license texts are emitted.
"""
import argparse
import hashlib
import json
import os
import re
import subprocess
from pathlib import Path

SCHEMA = 1
NOTICE_NAME = re.compile(r"^(licen[cs]e|copying|notice|copyright|patents)([._-].*)?$", re.I)


def require(condition, message):
    if not condition:
        raise ValueError(message)


def sha256(data):
    return "sha256:" + hashlib.sha256(data).hexdigest()


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True, ensure_ascii=False) + "\n", encoding="utf-8")


def read_json(path):
    return json.loads(path.read_text(encoding="utf-8"))


def command(*args, cwd=None):
    return subprocess.run(args, cwd=cwd, check=True, capture_output=True, text=True).stdout


def json_stream(text):
    decoder = json.JSONDecoder()
    while text.strip():
        value, end = decoder.raw_decode(text.lstrip())
        yield value
        text = text.lstrip()[end:]


def notice_files(directory):
    # Include nested originals (e.g. modernc SQLite and bundled npm sublibraries).
    # Never follow directory links or escape the dependency's source directory.
    directory = directory.resolve()
    for current, dirs, files in os.walk(directory):
        dirs[:] = sorted(d for d in dirs if d not in (".git", "node_modules")
                         and not (Path(current) / d).is_symlink())
        for name in sorted(files):
            if NOTICE_NAME.fullmatch(name):
                file = Path(current) / name
                require(file.resolve().is_relative_to(directory), "notice symlink escapes source: " + name)
                if file.is_file() and file.stat().st_size:
                    yield file


def copy_notices(source, output, prefix, root=None):
    records = []
    # notice_files() yields canonical paths, so the prefix must be canonical too
    # (macOS /var -> /private/var, Homebrew- or symlink-relocated module caches).
    source = source.resolve()
    # A symlinked top-level directory must not adopt its external target as the
    # allowed boundary; callers that know the owning tree pass it as `root`.
    if root is not None:
        require(source.is_relative_to(Path(root).resolve()), "notice directory escapes its root: " + prefix)
    for file in notice_files(source):
        relative = Path(prefix) / file.relative_to(source)
        data = file.read_bytes()
        target = output / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
        records.append({"path": relative.as_posix(), "sha256": sha256(data)})
    return sorted(records, key=lambda item: item["path"])


def dependency_id(name, version):
    # A digest suffix avoids collisions between scoped/path-like package names.
    key = name + "@" + version
    return re.sub(r"[^A-Za-z0-9._-]", "_", key) + "-" + hashlib.sha256(key.encode()).hexdigest()[:12]


def finish(output, component, ecosystem, dependencies, inputs):
    require(not output.joinpath("inventory.json").exists(), "output inventory already exists")
    identities = [(item["name"], item["version"]) for item in dependencies]
    require(len(set(identities)) == len(identities), "duplicate dependency identity")
    value = {"schemaVersion": SCHEMA, "component": component, "ecosystem": ecosystem,
             "inputs": inputs, "dependencies": sorted(dependencies, key=lambda item: (item["name"], item["version"]))}
    write_json(output / "inventory.json", value)
    return value


def collect_go(module, output, component):
    module = module.resolve()
    require((module / "go.mod").is_file(), "missing go.mod")
    dependencies = []
    metadata = json.loads(command("go", "mod", "edit", "-json", cwd=module))
    required = [item["Path"] for item in metadata.get("Require") or []]
    modules = json_stream(command("go", "list", "-m", "-json", *required, cwd=module)) if required else []
    for item in modules:
        require(not item.get("Replace"), "Go module replacements require explicit provenance support")
        require(item.get("Dir") and item.get("Sum"), "module not downloaded/verified: " + item["Path"])
        source = Path(item["Dir"])
        files = copy_notices(source, output, dependency_id(item["Path"], item["Version"]))
        require(files, "missing original Go license/NOTICE: " + item["Path"])
        dependencies.append({"name": item["Path"], "version": item["Version"],
                             "integrity": item["Sum"], "notices": files})
    goroot = Path(command("go", "env", "GOROOT", cwd=module).strip()).resolve()
    toolchain = command("go", "env", "GOVERSION", cwd=module).strip()
    files = []
    for name in ("LICENSE", "PATENTS"):
        source = goroot / name
        # Homebrew relocates the original LICENSE to the toolchain package root.
        if not source.is_file():
            source = goroot.parent / name
        if not source.is_file():
            continue
        require(source.resolve().is_relative_to(goroot.parent),
                "Go toolchain original escapes GOROOT: " + name)
        data = source.read_bytes()
        target = output / "go-toolchain" / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
        files.append({"path": "go-toolchain/" + name, "sha256": sha256(data)})
    require(any(item["path"].endswith("/LICENSE") for item in files), "missing Go standard-library license")
    for subtree in ("src/vendor", "src/crypto/internal/boring"):
        source = goroot / subtree
        if source.is_dir():
            files.extend(copy_notices(source, output, "go-toolchain/" + subtree, root=goroot))
    dependencies.append({"name": "Go standard library and bundled sources", "version": toolchain,
                         "notices": sorted(files, key=lambda item: item["path"])})
    inputs = {name: sha256((module / name).read_bytes()) for name in ("go.mod", "go.sum")
              if (module / name).is_file()}
    return finish(output, component, "go", dependencies, inputs)


def resolve_npm(root, parent, name):
    require(re.fullmatch(r"(?:@[a-zA-Z0-9._-]+/)?[a-zA-Z0-9._-]+", name), "unsafe npm package name")
    current = parent
    while current.is_relative_to(root):
        candidate = current / "node_modules" / name
        if (candidate / "package.json").is_file():
            require(candidate.resolve().is_relative_to(root), "npm package escapes build root")
            return candidate
        if current == root:
            break
        current = current.parent
    return None


def supplemental_notice(name, version, integrity, output):
    directory = Path(__file__).with_name("third-party-license-sources")
    manifest = read_json(directory / "manifest.json")
    matches = [item for item in manifest["packages"] if (item["name"], item["version"], item["integrity"])
               == (name, version, integrity)]
    require(len(matches) == 1, "missing pinned original npm license/NOTICE: " + name)
    item = matches[0]
    source = directory / item["path"]
    require(source.resolve().is_relative_to(directory.resolve()), "supplemental license escapes source root")
    data = source.read_bytes()
    require(data and sha256(data) == item["sha256"], "supplemental original license hash mismatch: " + name)
    relative = dependency_id(name, version) + "/LICENSE"
    target = output / relative
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(data)
    result = {key: item[key] for key in ("sha256", "source", "sourceCommit", "registry",
              "packageSourceCommit", "reason") if key in item}
    result["path"] = relative
    return [result]


def collect_web(root, output):
    root = root.resolve()
    manifest = root / "apps/web/package.json"
    lock = read_json(root / "package-lock.json")
    require(lock.get("lockfileVersion") in (2, 3), "unsupported npm lockfile schema")
    require(lock["packages"]["apps/web"]["dependencies"] == read_json(manifest)["dependencies"],
            "Web dependencies differ from lockfile")
    queue = [(root / "apps/web", name, False) for name in read_json(manifest)["dependencies"]]
    seen, dependencies = set(), {}
    while queue:
        parent, name, optional = queue.pop(0)
        source = resolve_npm(root, parent, name)
        if source is None and optional:
            continue
        require(source is not None, "missing installed Web dependency: " + name)
        if source in seen:
            continue
        seen.add(source)
        metadata = read_json(source / "package.json")
        pinned = lock["packages"].get(source.relative_to(root).as_posix())
        require(pinned and metadata.get("version") == pinned.get("version"),
                "installed npm package differs from locked version: " + name)
        require(metadata.get("name") == name, "npm package identity mismatch: " + name)
        identity = (name, metadata["version"])
        files = copy_notices(source, output, dependency_id(*identity), root=root)
        if not files:
            files = supplemental_notice(name, metadata["version"], pinned.get("integrity", ""), output)
        entry = {"name": name, "version": metadata["version"], "integrity": pinned.get("integrity", ""),
                 "integrityStatus": "present in lockfile" if pinned.get("integrity") else "integrity absent in lockfile",
                 "licenseExpression": metadata.get("license", pinned.get("license", "")), "notices": files}
        require(identity not in dependencies or dependencies[identity] == entry,
                "different installed bytes for one npm identity: " + name)
        dependencies[identity] = entry
        optionals = metadata.get("optionalDependencies", {})
        peers = metadata.get("peerDependenciesMeta", {})
        children = dict(metadata.get("dependencies", {}), **metadata.get("peerDependencies", {}))
        children.update(optionals)
        for child in sorted(children):
            queue.append((source, child, child in optionals or peers.get(child, {}).get("optional", False)))
    return finish(output, "web", "npm", list(dependencies.values()), {
        "apps/web/package.json": sha256(manifest.read_bytes()),
        "package-lock.json": sha256((root / "package-lock.json").read_bytes())})


def alpine_packages(database):
    result = []
    for block in database.read_text(encoding="utf-8").split("\n\n"):
        fields = dict(line.split(":", 1) for line in block.splitlines() if len(line) > 1 and line[1] == ":")
        if not fields:
            continue
        require(all(fields.get(key) for key in ("P", "V", "L")), "Alpine package lacks identity/license metadata")
        result.append({"name": fields["P"], "version": fields["V"], "licenseExpression": fields["L"],
                       "source": fields.get("U", ""), "origin": fields.get("o", fields["P"]),
                       "notices": [], "evidence": "installed-apk-database"})
    require(result, "empty installed Alpine package database")
    return result


def collect_base(root, kind, output):
    root = root.resolve()
    if kind == "alpine":
        database = root / "lib/apk/db/installed"
        dependencies = alpine_packages(database)
        # Alpine records SPDX expressions in APK; unlike Debian it does not
        # promise original license texts for every installed binary package.
        notices = root / "usr/share/licenses"
        if notices.is_dir():
            files = copy_notices(notices, output, "packaged-notices", root=root)
            for item in dependencies:
                item["notices"] = [f for f in files if f["path"].startswith("packaged-notices/" + item["name"] + "/")]
        inputs = {"lib/apk/db/installed": sha256(database.read_bytes())}
    else:
        database = root / "base-packages.tsv"
        dependencies = []
        for line in database.read_text(encoding="utf-8").splitlines():
            name, version, source = line.split("\t")
            doc = root / "usr/share/doc" / name.split(":")[0] / "copyright"
            require(doc.is_file() and doc.stat().st_size, "missing Debian copyright: " + name)
            require(doc.resolve().is_relative_to(root), "Debian copyright escapes package root")
            relative = dependency_id(name, version) + "/copyright"
            destination = output / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_bytes(doc.read_bytes())
            dependencies.append({"name": name, "version": version, "source": source,
                                 "notices": [{"path": relative, "sha256": sha256(doc.read_bytes())}],
                                 "evidence": "installed-dpkg-database-and-copyright"})
        require(dependencies, "empty installed Debian package database")
        inputs = {"base-packages.tsv": sha256(database.read_bytes())}
    return finish(output, "base-image", kind, dependencies, inputs)


def source_inventory(root):
    paths = ["apps/manager-server/go.mod", "apps/manager-server/go.sum", "apps/ingress/go.mod",
             "apps/runtime-supervisor/go.mod", "apps/runtime-supervisor/go.sum",
             "apps/web/package.json", "package-lock.json"]
    modules = {}
    for component, directory in (("manager", "apps/manager-server"), ("ingress", "apps/ingress"),
                                 ("runtime-supervisor", "apps/runtime-supervisor")):
        metadata = json.loads(command("go", "mod", "edit", "-json", cwd=root / directory))
        modules[component] = sorted([{"name": item["Path"], "version": item["Version"]}
                                     for item in metadata.get("Require") or []], key=lambda item: item["name"])
    return {"schemaVersion": SCHEMA, "status": "SOURCE_MANIFESTS_VERIFIED",
            "collectionPolicy": "collect-third-party-licenses.py; originals and SHA256 inventories required at image build",
            "basePackageEvidence": {"alpine": "exact installed APK package versions and SPDX license expressions; available original notices",
                                    "debian": "exact installed dpkg package versions and original copyright files"},
            "goModules": modules,
            "webDependencies": read_json(root / "apps/web/package.json")["dependencies"],
            "inputs": {name: sha256((root / name).read_bytes()) for name in paths}}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="mode", required=True)
    go = sub.add_parser("go")
    go.add_argument("--module", type=Path, required=True)
    go.add_argument("--component", choices=("manager", "ingress", "runtime-supervisor"), required=True)
    web = sub.add_parser("web")
    web.add_argument("--root", type=Path, required=True)
    base = sub.add_parser("base")
    base.add_argument("--kind", choices=("alpine", "debian"), required=True)
    base.add_argument("--root", type=Path, required=True)
    source = sub.add_parser("source")
    source.add_argument("--root", type=Path, required=True)
    for child in (go, web, base, source):
        child.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if args.mode == "go":
        collect_go(args.module, args.output, args.component)
    elif args.mode == "web":
        collect_web(args.root, args.output)
    elif args.mode == "base":
        collect_base(args.root, args.kind, args.output)
    else:
        write_json(args.output, source_inventory(args.root))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, KeyError, subprocess.CalledProcessError) as exc:
        raise SystemExit("STOP: license inventory incomplete: " + str(exc)) from exc
