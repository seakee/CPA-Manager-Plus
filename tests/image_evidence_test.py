#!/usr/bin/env python3
"""Offline release-evidence tests; fixtures are not real license clearance."""
import json
import os
import re
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from image_evidence_fixtures import (ROOT, SHA, SOURCE_BYTES, VERSION, base_files,
                                     collector, evidence, make_evidence)


class CollectorTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.output = self.root / "out"

    def put(self, relative, data):
        path = self.root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data if isinstance(data, bytes) else data.encode())
        return path

    def npm_fixture(self):
        manifest = {"dependencies": {"direct": "1.0.0"}, "devDependencies": {"tool": "1.0.0"}}
        self.put("apps/web/package.json", json.dumps(manifest))
        lock = {"lockfileVersion": 3, "packages": {"apps/web": manifest}}
        for relative, name, version, extra in (
                ("node_modules/direct", "direct", "1.0.0", {"dependencies": {"transitive": "2.0.0"},
                    "optionalDependencies": {"not-installed": "1.0.0"},
                    "peerDependencies": {"peer": "3.0.0", "optional-peer": "1"},
                    "peerDependenciesMeta": {"optional-peer": {"optional": True}}}),
                ("node_modules/direct/node_modules/transitive", "transitive", "2.0.0", {}),
                ("node_modules/peer", "peer", "3.0.0", {}),
                ("node_modules/tool", "tool", "1.0.0", {})):
            package = dict(name=name, version=version, license="MIT", **extra)
            self.put(relative + "/package.json", json.dumps(package))
            self.put(relative + "/LICENSE", "original " + name + " license\n")
            self.put(relative + "/NOTICE", "original " + name + " notice\n")
            lock["packages"][relative] = {"version": version, "integrity": "sha512-fixture"}
        self.put("package-lock.json", json.dumps(lock))
        return lock

    def go_fixture(self):
        module = self.put("module/go.mod", "module example.test/main\n").parent
        self.put("module/go.sum", "example.test/lib v1.0.0 h1:fixture\n")
        self.put("lib/LICENSE", "original module license\n")
        self.put("lib/sub/NOTICE", "original nested notice\n")
        self.put("go/LICENSE", "original Go BSD license\n")
        self.put("go/PATENTS", "original Go patents\n")
        self.put("go/src/vendor/example.test/LICENSE", "original vendored license\n")
        item = {"Path": "example.test/lib", "Version": "v1.0.0", "Sum": "h1:fixture", "Dir": str(self.root / "lib")}
        def fake_command(*args, **kwargs):
            if args[1:3] == ("mod", "edit"):
                return json.dumps({"Require": [{"Path": "example.test/lib", "Version": "v1.0.0"}]})
            if args[1] == "list":
                self.assertEqual(args[-1], "example.test/lib")
                return json.dumps(item)
            return str(self.root / "go") if args[-1] == "GOROOT" else "go1.24.0"
        return module, item, fake_command

    def test_web_closure_includes_nested_transitive_peers_not_dev(self):
        self.npm_fixture()
        result = collector.collect_web(self.root, self.output)
        self.assertEqual([item["name"] for item in result["dependencies"]], ["direct", "peer", "transitive"])
        self.assertTrue(all(len(item["notices"]) == 2 for item in result["dependencies"]))
        for item in result["dependencies"]:
            for notice in item["notices"]:
                self.assertEqual(collector.sha256((self.output / notice["path"]).read_bytes()), notice["sha256"])
        second = self.root / "second"
        collector.collect_web(self.root, second)
        self.assertEqual((self.output / "inventory.json").read_bytes(), (second / "inventory.json").read_bytes())
        self.assertNotIn(str(self.root), (self.output / "inventory.json").read_text())

    def test_web_records_lock_entries_without_registry_integrity(self):
        lock = self.npm_fixture()
        del lock["packages"]["node_modules/direct"]["integrity"]
        self.put("package-lock.json", json.dumps(lock))
        result = collector.collect_web(self.root, self.output)
        self.assertEqual(result["dependencies"][0]["integrity"], "")

    def test_web_missing_required_dependency_fails(self):
        self.npm_fixture()
        (self.root / "node_modules/peer/package.json").unlink()
        with self.assertRaisesRegex(ValueError, "missing installed Web dependency"):
            collector.collect_web(self.root, self.output)

    def test_web_version_mismatch_fails(self):
        lock = self.npm_fixture()
        lock["packages"]["node_modules/direct"]["version"] = "9.0.0"
        self.put("package-lock.json", json.dumps(lock))
        with self.assertRaisesRegex(ValueError, "locked version"):
            collector.collect_web(self.root, self.output)

    def test_web_lock_manifest_mismatch_fails(self):
        lock = self.npm_fixture()
        lock["packages"]["apps/web"]["dependencies"] = {}
        self.put("package-lock.json", json.dumps(lock))
        with self.assertRaisesRegex(ValueError, "differ from lockfile"):
            collector.collect_web(self.root, self.output)

    def test_web_missing_original_fails_without_synthesizing_spdx_text(self):
        self.npm_fixture()
        for name in ("LICENSE", "NOTICE"):
            (self.root / "node_modules/direct" / name).unlink()
        with self.assertRaisesRegex(ValueError, "missing pinned original"):
            collector.collect_web(self.root, self.output)

    def test_pinned_supplemental_originals_verify_exact_artifact_and_text(self):
        directory = ROOT / "bin/release/third-party-license-sources"
        for item in collector.read_json(directory / "manifest.json")["packages"]:
            result = collector.supplemental_notice(item["name"], item["version"], item["integrity"], self.output)
            self.assertEqual(result[0]["sha256"], collector.sha256((directory / item["path"]).read_bytes()))
            self.assertEqual(result[0]["sourceCommit"], item["sourceCommit"])
            with self.assertRaisesRegex(ValueError, "missing pinned original"):
                collector.supplemental_notice(item["name"], "99.0.0", item["integrity"], self.output)
            with self.assertRaisesRegex(ValueError, "missing pinned original"):
                collector.supplemental_notice(item["name"], item["version"], "wrong-integrity", self.output)

    def test_supplemental_allowlist_is_exactly_three_and_hash_mismatch_fails(self):
        manifest = collector.read_json(ROOT / "bin/release/third-party-license-sources/manifest.json")
        self.assertEqual({item["name"] for item in manifest["packages"]}, {
            "@uiw/react-codemirror", "@uiw/codemirror-extensions-basic-setup", "html-parse-stringify"})
        item = manifest["packages"][0]
        item["sha256"] = "sha256:" + "0" * 64
        with patch.object(collector, "read_json", return_value=manifest), self.assertRaisesRegex(ValueError, "hash mismatch"):
            collector.supplemental_notice(item["name"], item["version"], item["integrity"], self.output)

    def test_go_collects_declared_modules_toolchain_nested_originals(self):
        module, item, fake = self.go_fixture()
        with patch.object(collector, "command", fake):
            result = collector.collect_go(module, self.output, "manager")
        self.assertEqual(len(result["dependencies"]), 2)
        dependency = next(dep for dep in result["dependencies"] if dep["name"] == item["Path"])
        self.assertEqual(dependency["integrity"], "h1:fixture")
        self.assertEqual(len(dependency["notices"]), 2)
        self.assertEqual(len(result["inputs"]), 2)
        self.assertIn("original module license", (self.output / dependency["notices"][0]["path"]).read_text())

    def test_go_missing_original_fails(self):
        module, _, fake = self.go_fixture()
        for path in ("lib/LICENSE", "lib/sub/NOTICE"):
            (self.root / path).unlink()
        with patch.object(collector, "command", fake), self.assertRaisesRegex(ValueError, "missing original Go"):
            collector.collect_go(module, self.output, "manager")

    def test_go_undownloaded_module_fails(self):
        module, item, fake = self.go_fixture()
        del item["Dir"]
        with patch.object(collector, "command", fake), self.assertRaisesRegex(ValueError, "not downloaded/verified"):
            collector.collect_go(module, self.output, "manager")

    def test_go_replacement_fails(self):
        module, item, fake = self.go_fixture()
        item["Replace"] = {"Dir": "local"}
        with patch.object(collector, "command", fake), self.assertRaisesRegex(ValueError, "replacements"):
            collector.collect_go(module, self.output, "manager")

    def test_go_missing_toolchain_original_fails(self):
        module, _, fake = self.go_fixture()
        (self.root / "go/LICENSE").unlink()
        with patch.object(collector, "command", fake), self.assertRaisesRegex(ValueError, "standard-library license"):
            collector.collect_go(module, self.output, "manager")

    def test_go_homebrew_relocated_original_is_preserved(self):
        module, _, fake = self.go_fixture()
        (self.root / "go/LICENSE").rename(self.root / "LICENSE")
        with patch.object(collector, "command", fake):
            collector.collect_go(module, self.output, "manager")
        self.assertEqual((self.output / "go-toolchain/LICENSE").read_bytes(), (self.root / "LICENSE").read_bytes())

    def test_go_toolchain_original_symlink_escape_fails(self):
        module, _, fake = self.go_fixture()
        external = tempfile.TemporaryDirectory()
        self.addCleanup(external.cleanup)
        license_path = Path(external.name) / "LICENSE"
        license_path.write_text("external license\n")
        (self.root / "go/LICENSE").unlink()
        (self.root / "go/LICENSE").symlink_to(license_path)
        with patch.object(collector, "command", fake), \
                self.assertRaisesRegex(ValueError, "escapes GOROOT"):
            collector.collect_go(module, self.output, "manager")

    def test_go_bundled_subtree_symlink_escape_fails(self):
        module, _, fake = self.go_fixture()
        external = tempfile.TemporaryDirectory()
        self.addCleanup(external.cleanup)
        vendored = Path(external.name) / "example.test"
        vendored.mkdir()
        (vendored / "LICENSE").write_text("external vendored license\n")
        shutil.rmtree(self.root / "go/src/vendor")
        (self.root / "go/src/vendor").symlink_to(Path(external.name))
        with patch.object(collector, "command", fake), \
                self.assertRaisesRegex(ValueError, "notice directory escapes its root"):
            collector.collect_go(module, self.output, "manager")

    def test_notice_symlink_escape_fails(self):
        self.put("outside", "not a dependency original")
        source = self.root / "dependency"
        source.mkdir()
        (source / "LICENSE").symlink_to(self.root / "outside")
        with self.assertRaisesRegex(ValueError, "escapes source"):
            collector.copy_notices(source, self.output, "dependency")

    def test_alpine_records_exact_final_packages_and_spdx(self):
        self.put("lib/apk/db/installed", "P:musl\nV:1.2.5-r9\nL:MIT\nU:https://musl.libc.org\no:musl\n\nP:busybox\nV:1.37-r1\nL:GPL-2.0-only\n")
        result = collector.collect_base(self.root, "alpine", self.output)
        self.assertEqual([item["name"] for item in result["dependencies"]], ["busybox", "musl"])
        self.assertEqual(result["dependencies"][0]["licenseExpression"], "GPL-2.0-only")
        self.assertTrue(all(item["evidence"] == "installed-apk-database" for item in result["dependencies"]))

    def test_alpine_missing_license_metadata_fails(self):
        database = self.put("lib/apk/db/installed", "P:musl\nV:1.2.5-r9\n")
        with self.assertRaisesRegex(ValueError, "lacks identity/license"):
            collector.alpine_packages(database)

    def test_alpine_packaged_notices_symlink_escape_fails(self):
        self.put("lib/apk/db/installed", "P:musl\nV:1.2.5-r9\nL:MIT\n")
        external = tempfile.TemporaryDirectory()
        self.addCleanup(external.cleanup)
        (Path(external.name) / "LICENSE").write_text("external license\n")
        (self.root / "usr/share").mkdir(parents=True)
        (self.root / "usr/share/licenses").symlink_to(Path(external.name))
        with self.assertRaisesRegex(ValueError, "notice directory escapes its root"):
            collector.collect_base(self.root, "alpine", self.output)

    def test_alpine_packaged_notices_inside_root_are_copied(self):
        self.put("lib/apk/db/installed", "P:musl\nV:1.2.5-r9\nL:MIT\n")
        self.put("usr/share/licenses/musl/COPYING", "original musl license\n")
        result = collector.collect_base(self.root, "alpine", self.output)
        self.assertEqual(result["dependencies"][0]["notices"][0]["path"], "packaged-notices/musl/COPYING")

    def test_debian_original_copyright_including_relative_symlink(self):
        self.put("base-packages.tsv", "libc6:amd64\t2.36-r1\tglibc\nlibc-bin\t2.36-r1\tglibc\n")
        self.put("usr/share/doc/libc6/copyright", "original Debian copyright\n")
        (self.root / "usr/share/doc/libc-bin").mkdir()
        (self.root / "usr/share/doc/libc-bin/copyright").symlink_to("../libc6/copyright")
        result = collector.collect_base(self.root, "debian", self.output)
        self.assertEqual(len(result["dependencies"]), 2)
        self.assertTrue(all(dep["notices"] for dep in result["dependencies"]))

    def test_debian_missing_original_fails(self):
        self.put("base-packages.tsv", "libc6:amd64\t2.36-r1\tglibc\n")
        with self.assertRaisesRegex(ValueError, "missing Debian copyright"):
            collector.collect_base(self.root, "debian", self.output)

    def test_empty_base_database_fails(self):
        for kind, path in (("alpine", "lib/apk/db/installed"), ("debian", "base-packages.tsv")):
            self.put(path, "")
            with self.subTest(kind=kind), self.assertRaisesRegex(ValueError, "empty installed"):
                collector.collect_base(self.root, kind, self.output / kind)

    def test_checked_source_inventory_matches_actual_manifests(self):
        actual = (ROOT / "bin/release/embedded-third-party-inventory.txt").read_bytes()
        self.assertEqual(actual, evidence.encoded(collector.source_inventory(ROOT)))
        self.assertNotIn(b"NOT_VERIFIED", actual)
        value = json.loads(actual)
        self.assertTrue(value["goModules"]["manager"])
        self.assertEqual(value["goModules"]["ingress"], [])
        self.assertTrue(value["webDependencies"])


class ImageEvidenceTests(unittest.TestCase):
    def fixture(self, role="manager"):
        files = make_evidence(role, base_files(role))
        return files, json.loads(files[evidence.INVENTORY]), json.loads(files[evidence.PROVENANCE])

    def test_all_three_roles_generate_complete_deterministic_evidence(self):
        for role in evidence.COMPONENTS:
            with self.subTest(role=role):
                files, inventory, provenance = self.fixture(role)
                evidence.validate_inventory(inventory, role, files.__getitem__, SOURCE_BYTES)
                evidence.validate_provenance(provenance, role, VERSION, SHA, files.__getitem__)
                self.assertEqual(files, make_evidence(role, base_files(role)))
                self.assertEqual(set(inventory["components"]), set(evidence.COMPONENTS[role]))
                self.assertIn(evidence.LICENSE_ROOT + "cpamp/LICENSE", files)
                if role == "runtime":
                    self.assertIn(evidence.LICENSE_ROOT + "cli-proxy-api/LICENSE", files)

    def test_invalid_version_and_source_claims_fail(self):
        for version, commit in (("", SHA), ("unknown", SHA), ("UNKNOWN", SHA), ("bad version", SHA),
                                (VERSION, ""), (VERSION, "unknown"), (VERSION, "a" * 39), (VERSION, "z" * 40)):
            with self.subTest(version=version, commit=commit), self.assertRaises(ValueError):
                evidence.identity(version, commit)

    def test_notice_tampering_fails(self):
        files, inventory, _ = self.fixture()
        files[evidence.notice_path("manager", "fixture/LICENSE")] += b"changed"
        with self.assertRaisesRegex(ValueError, "license text hash mismatch"):
            evidence.validate_inventory(inventory, "manager", files.__getitem__)

    def test_empty_missing_component_inventory_fails(self):
        files, inventory, _ = self.fixture()
        del inventory["components"]["web"]
        with self.assertRaisesRegex(ValueError, "incomplete component"):
            evidence.validate_inventory(inventory, "manager", files.__getitem__)

    def change_component(self, inventory, files, component, change):
        value = inventory["components"][component]["inventory"]
        change(value)
        raw = evidence.encoded(value)
        files[evidence.notice_path(component, "inventory.json")] = raw
        inventory["components"][component]["inventorySHA256"] = evidence.digest(raw)

    def test_missing_go_module_coverage_fails_even_with_rehashed_inventory(self):
        files, inventory, _ = self.fixture()
        self.change_component(inventory, files, "manager", lambda value: value["dependencies"].pop(0))
        with self.assertRaisesRegex(ValueError, "declared Go module coverage"):
            evidence.validate_inventory(inventory, "manager", files.__getitem__)

    def test_wrong_go_input_hash_fails(self):
        files, inventory, _ = self.fixture()
        self.change_component(inventory, files, "manager", lambda value: value["inputs"].update({"go.mod": "sha256:" + "b" * 64}))
        with self.assertRaisesRegex(ValueError, "Go license inputs differ"):
            evidence.validate_inventory(inventory, "manager", files.__getitem__)

    def test_missing_original_notices_fails(self):
        files, inventory, _ = self.fixture()
        self.change_component(inventory, files, "web", lambda value: value["dependencies"][0].update(notices=[]))
        with self.assertRaisesRegex(ValueError, "missing original dependency"):
            evidence.validate_inventory(inventory, "manager", files.__getitem__)

    def test_inventory_traversal_fails(self):
        files, inventory, _ = self.fixture()
        self.change_component(inventory, files, "web", lambda value: value["dependencies"][0]["notices"][0].update(path="../cpamp/LICENSE"))
        with self.assertRaisesRegex(ValueError, "unsafe license inventory path"):
            evidence.validate_inventory(inventory, "manager", files.__getitem__)

    def test_provenance_schema_identity_and_component_coverage_fail_closed(self):
        files, _, provenance = self.fixture()
        for key, value in (("schemaVersion", True), ("schemaVersion", 2), ("sourceCommit", "b" * 40),
                           ("version", "unknown"), ("platform", "darwin/arm64"), ("components", []),
                           ("licenseInventorySHA256", "sha256:" + "b" * 64)):
            candidate = dict(provenance, **{key: value})
            with self.subTest(key=key), self.assertRaises(ValueError):
                evidence.validate_provenance(candidate, "manager", VERSION, SHA, files.__getitem__)

    def test_provenance_binds_final_binary_and_web_manifest_bytes(self):
        for path in ("/usr/local/bin/cpa-manager-plus", "/usr/local/share/cpamp/release/fullweb-manifest.json"):
            files, _, provenance = self.fixture()
            files[path] += b"changed"
            with self.subTest(path=path), self.assertRaisesRegex(ValueError, "component hash mismatch"):
                evidence.validate_provenance(provenance, "manager", VERSION, SHA, files.__getitem__)

    def test_runtime_cpa_provenance_binds_upstream_artifact(self):
        files, _, provenance = self.fixture("runtime")
        artifact = json.loads(files["/usr/local/share/cpamp/runtime/cpa-artifact.json"])
        artifact["version"] = "0.0.0"
        files["/usr/local/share/cpamp/runtime/cpa-artifact.json"] = evidence.encoded(artifact)
        with self.assertRaisesRegex(ValueError, "CPA provenance differs"):
            evidence.validate_provenance(provenance, "runtime", VERSION, SHA, files.__getitem__)


class DockerContracts(unittest.TestCase):
    GUARD = re.compile(r'RUN set -eu; \\\n(?:    [^\n]+\n)*?    case "\$SOURCE_COMMIT" in [^\n]*\n')
    IDENTITY_ARGS = ("VERSION", "SOURCE_COMMIT", "TARGETOS", "TARGETARCH")

    def test_all_dockerfiles_guard_identity_label_and_package_evidence(self):
        identity_cases = (
            (VERSION, SHA, True), ("2.0.0-beta.1", SHA, True), ("a" * 128, SHA, True),
            ("", SHA, False), ("unknown", SHA, False), ("UNKNOWN", SHA, False),
            ("bad value", SHA, False), ("-leading", SHA, False), ("a" * 129, SHA, False),
            (VERSION, "", False), (VERSION, "unknown", False), (VERSION, "abc", False),
            (VERSION, SHA.upper(), False), (VERSION, "a" * 128, False),
        )
        for name in ("manager-server", "ingress", "runtime"):
            text = (ROOT / ("Dockerfile." + name)).read_text()
            with self.subTest(image=name):
                self.assertNotIn("ARG VERSION=", text)
                self.assertNotIn("ARG SOURCE_COMMIT=", text)
                # The weak early guards must not survive beside the strict one.
                self.assertNotIn('test "$VERSION" != unknown', text)
                self.assertNotIn('test "$SOURCE_COMMIT" != unknown', text)
                for label in ("version=$VERSION", "revision=$SOURCE_COMMIT", 'source="' + evidence.SOURCE + '"'):
                    self.assertIn("org.opencontainers.image." + label, text)
                self.assertIn("COPY LICENSE /image/usr/local/share/licenses/cpamp/LICENSE", text)
                self.assertIn("/image/usr/local/share/licenses/ /usr/local/share/licenses/", text)
                self.assertIn("/image/usr/local/share/cpamp/ /usr/local/share/cpamp/", text)
                self.assertIn("/tools/image-evidence.py", text)
                guards = self.GUARD.findall(text)
                self.assertEqual(len(guards), 1, "expected exactly one early identity guard")
                guard = guards[0]
                # The guard must run before the first Web or Go compile.
                marker = min(text.index(needle) for needle in ("npm run build", "go build") if needle in text)
                self.assertLess(text.index(guard), marker, "identity guard must precede the first build")
                shell = guard[len("RUN "):]
                for version, commit, success in identity_cases:
                    result = subprocess.run(
                        ["sh", "-c", shell],
                        env=dict(os.environ, VERSION=version, SOURCE_COMMIT=commit),
                        capture_output=True)
                    with self.subTest(version=version, commit=commit):
                        self.assertEqual(result.returncode == 0, success, result.stderr)
        self.assertIn('RUN VERSION="$VERSION" npm run build',
                      (ROOT / "Dockerfile.manager-server").read_text())

    def test_every_image_evidence_stage_declares_identity_args(self):
        # ARG is stage-scoped: a stage that runs image-evidence.py must declare
        # the arguments it reads in its own FROM block.
        for name in ("manager-server", "ingress", "runtime"):
            text = (ROOT / ("Dockerfile." + name)).read_text()
            stages = [block for block in text.split("\nFROM ")[1:] if "image-evidence.py" in block]
            with self.subTest(image=name):
                self.assertTrue(stages, "no stage runs the evidence generator")
            for block in stages:
                header, _, body = block.partition("\n")
                declared = re.findall(r"^ARG (\w+)$", body, re.M)
                with self.subTest(image=name, stage=header.split(" AS ")[-1]):
                    for argument in self.IDENTITY_ARGS:
                        self.assertIn(argument, declared)

    def test_base_package_snapshot_precedes_audit_dependency_install(self):
        for name, stage, snapshot, interpreter in (
                ("manager-server", "FROM manager-base AS manager-evidence", "cp /lib/apk/db/installed", "apk add --no-cache python3"),
                ("ingress", "FROM ingress-base AS ingress-evidence", "cp /lib/apk/db/installed", "apk add --no-cache python3"),
                ("runtime", "FROM runtime-os AS runtime-evidence", "dpkg-query -W", "apt-get install -y --no-install-recommends python3")):
            text = (ROOT / ("Dockerfile." + name)).read_text().split(stage, 1)[1]
            with self.subTest(image=name):
                self.assertLess(text.index(snapshot), text.index(interpreter))
                self.assertIn("base --kind", text)

    def test_runtime_preserves_cpa_original_in_all_targets(self):
        text = (ROOT / "Dockerfile.runtime").read_text()
        self.assertIn('tar -xOzf "/tmp/${asset_name}" LICENSE > /out/CPA-LICENSE', text)
        for stage in ("v8-amd64-candidate", "v8-amd64-loopback-proof", "v7-compatibility", "runtime"):
            block = text.split(" AS " + stage + "\n", 1)[1].split("\nFROM ", 1)[0]
            with self.subTest(stage=stage):
                self.assertIn("/usr/local/share/licenses/cli-proxy-api/LICENSE", block)

    def test_candidate_builder_passes_real_args_for_all_roles(self):
        text = (ROOT / "bin/release/build-embedded-candidate.sh").read_text()
        block = text.split("  args=(", 1)[1].split("\n  )", 1)[0]
        self.assertIn('--build-arg "SOURCE_COMMIT=$source_sha"', block)
        self.assertIn("--build-arg VERSION=2.0.0-r1candidate", block)
        subprocess.run(["bash", "-n", "bin/release/build-embedded-candidate.sh"], cwd=ROOT, check=True)


if __name__ == "__main__":
    unittest.main()
