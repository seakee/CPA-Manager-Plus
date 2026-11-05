"""Synthetic license/provenance fixtures; never real dependency clearance."""
import importlib.util
import json
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def load_script(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), ROOT / "bin/release" / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


evidence = load_script("image-evidence")
collector = load_script("collect-third-party-licenses")
SHA = "a" * 40
VERSION = "2.0.0-r1candidate"
SOURCE = {"schemaVersion": 1, "status": "SOURCE_MANIFESTS_VERIFIED",
          "inputs": {directory + "/go.mod": evidence.digest(b"fixture go.mod")
                     for directory in evidence.MODULES.values()},
          "goModules": {component: [{"name": "example.test/module", "version": "v1.0.0"}]
                        for component in evidence.MODULES},
          "webDependencies": {"example-web": "1.0.0"}}
SOURCE["inputs"].update({name: evidence.digest(b"fixture " + name.encode())
                         for name in ("apps/web/package.json", "package-lock.json")})
SOURCE_BYTES = evidence.encoded(SOURCE)


def make_evidence(role, files, source_bytes=SOURCE_BYTES):
    """Use the real generator on synthetic originals and caller-supplied binaries."""
    files = dict(files)
    files.setdefault(evidence.LICENSE_ROOT + "cpamp/LICENSE", b"MIT fixture CPAMP license\n")
    for component in evidence.COMPONENTS[role]:
        data = b"original fixture dependency notice\n"
        notices = [{"path": "fixture/LICENSE", "sha256": evidence.digest(data)}]
        dependencies = [{"name": "example.test/module", "version": "v1.0.0", "notices": notices}]
        inputs, ecosystem = {}, "go"
        if component in evidence.MODULES:
            dependencies.append({"name": "Go standard library and bundled sources", "version": "go1.24.0",
                                 "notices": notices})
            inputs = {"go.mod": SOURCE["inputs"][evidence.MODULES[component] + "/go.mod"]}
        elif component == "web":
            ecosystem = "npm"
            dependencies = [{"name": "example-web", "version": "1.0.0", "notices": notices}]
            inputs = {name: SOURCE["inputs"][name] for name in ("apps/web/package.json", "package-lock.json")}
        else:
            ecosystem = "alpine"
            dependencies = [{"name": "musl", "version": "1.2.5-r1", "licenseExpression": "MIT",
                             "evidence": "installed-apk-database", "notices": []}]
        value = {"schemaVersion": 1, "component": component, "ecosystem": ecosystem,
                 "inputs": inputs, "dependencies": dependencies}
        files[evidence.notice_path(component, "inventory.json")] = evidence.encoded(value)
        files[evidence.notice_path(component, "fixture/LICENSE")] = data
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        for path, data in files.items():
            destination = root / path.lstrip("/")
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_bytes(data)
        source = root / "source.json"
        source.write_bytes(source_bytes)
        evidence.build(root, role, VERSION, SHA, "linux/amd64", source)
        return {"/" + path.relative_to(root).as_posix(): path.read_bytes()
                for path in root.rglob("*") if path.is_file() and path != source}


def base_files(role):
    files = {"/usr/local/bin/" + binary: ("fixture " + name).encode()
             for name, binary in evidence.BINARIES[role]}
    if role == "runtime":
        files["/usr/local/share/cpamp/runtime/cpa-artifact.json"] = evidence.encoded({
            "schemaVersion": 1, "engine": "cpa", "version": "8.0.13",
            "artifactId": evidence.digest(files["/usr/local/bin/cli-proxy-api"])})
        files[evidence.LICENSE_ROOT + "cli-proxy-api/LICENSE"] = b"MIT fixture CPA license\n"
    elif role == "manager":
        files["/usr/local/share/cpamp/release/fullweb-manifest.json"] = b'{"schemaVersion":1,"files":[]}'
    return files
