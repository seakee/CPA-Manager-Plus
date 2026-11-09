#!/usr/bin/env python3
"""Contract tests for disposable R1-1 OCI verifier; no Docker or network."""
import gzip
import hashlib
import importlib.util
import io
import json
import tarfile
import tempfile
import unittest
import os
import re
import subprocess
import sys
from unittest.mock import patch
from embedded_renderer_fixture import stage_renderer, redirect_fixture_node
from pathlib import Path

from image_evidence_fixtures import SOURCE_BYTES, make_evidence
VERIFIER = Path(__file__).resolve().parents[1] / "bin/release/verify-embedded-candidate.py"
spec = importlib.util.spec_from_file_location("embedded_candidate", VERIFIER)
subject = importlib.util.module_from_spec(spec)
spec.loader.exec_module(subject)
SHA = "a" * 40
EVENT = "local-test-event"
MOCK_RUNTIME_ENTRYPOINT = b"#!/bin/sh\n"
MOCK_API_CONTRACT = json.dumps({
    "schemaVersion": 1, "qualification": subject.API_CONTRACT_QUALIFICATION,
    "apiSuffix": "/api/v2", "expectedPath": "/phase4-test/api/v2/technical",
}).encode()


def blob(data):
    return "sha256:" + hashlib.sha256(data).hexdigest()


def tar_content(files, bad_mode=None):
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode="w") as archive:
        for path, data in files.items():
            entry = tarfile.TarInfo(path.lstrip("/"))
            entry.size = len(data)
            entry.mode = 0o755 if path.startswith("/usr/local/bin/") else 0o644
            if path == bad_mode:
                entry.mode = 0o644
            archive.addfile(entry, io.BytesIO(data))
    return output.getvalue()


def sample_elf():
    # The 64-byte header was the pre-fix false positive: not a loadable binary.
    data = bytearray(64)
    data[0:4] = b"\x7fELF"
    data[4:7] = b"\x02\x01\x01"
    data[16:18] = (2).to_bytes(2, "little")
    data[18:20] = (62).to_bytes(2, "little")
    data[20:24] = (1).to_bytes(4, "little")
    return bytes(data)


ROLE_BINARIES = {}


def make_oci(destination, role, license_bytes, *,
             source=SHA, event=EVENT, arch="amd64", corrupt=False, missing=None,
             bad_descriptor_size=False, bad_descriptor_type=False, bad_diff_id=False,
             source_inventory=SOURCE_BYTES, api_contract=MOCK_API_CONTRACT,
             bad_mode=None, invalid_elf=False, wrong_module=False, wrong_source_stamp=False,
             wrong_sha_suffix=False, wrong_version_suffix=False, wrong_entrypoint=False,
             wrong_cmd=False, runtime_script=MOCK_RUNTIME_ENTRYPOINT, mutate_evidence=None):
    cpa_bundled = role == "full"
    files = {
        "/usr/local/share/licenses/cpamp/LICENSE": license_bytes,
        "/usr/local/bin/cpamp-launcher": ROLE_BINARIES["launcher"],
        "/usr/local/bin/cpa-manager-plus": (
            sample_elf() if invalid_elf else
            ROLE_BINARIES["ingress"] if wrong_module else
            ROLE_BINARIES["manager_wrong_stamp"] if wrong_source_stamp else
            ROLE_BINARIES["manager_sha_suffix"] if wrong_sha_suffix else
            ROLE_BINARIES["manager_version_suffix"] if wrong_version_suffix else
            ROLE_BINARIES["manager"]
        ),
        "/usr/local/bin/cpamp-ingress": ROLE_BINARIES["ingress"],
        "/usr/local/bin/cpamp-runtime-supervisor": ROLE_BINARIES["runtime-supervisor"],
        subject.API_CONTRACT: api_contract,
        subject.WEB_MANIFEST: json.dumps({
            "schemaVersion": 1, "files": [
                {"path": "index.html", "sha256": blob(b"<html/>")},
                {"path": "assets/a.js", "sha256": blob(b"x")},
            ],
        }).encode(),
        "/usr/local/share/cpamp/runtime/cpa-artifact.json": json.dumps({
            "schemaVersion": 1, "engine": "cpa", "version": "8.0.55",
            "artifactId": blob(ROLE_BINARIES["ingress"]),
        }).encode(),
        "/usr/local/share/cpamp/runtime/config.seed.yaml": b"seed",
    }
    if cpa_bundled:
        files["/usr/local/bin/cli-proxy-api"] = ROLE_BINARIES["ingress"]
        files["/usr/local/share/licenses/cli-proxy-api/LICENSE"] = b"official"
    files = make_evidence("cpamp", files, source_inventory, cpa_bundled=cpa_bundled)
    if mutate_evidence is not None:
        files = mutate_evidence(files)
    if missing:
        files.pop(missing)
    uncompressed = tar_content(files, bad_mode=bad_mode)
    layer = gzip.compress(uncompressed)
    layer_id = blob(layer)
    diff_id = blob(uncompressed)
    entrypoint, command = subject.STARTUP[role]
    config = json.dumps({
        "os": "linux", "architecture": arch,
        "rootfs": {"type": "layers", "diff_ids": [
            "sha256:" + "d" * 64 if bad_diff_id else diff_id,
        ]},
        "config": {
            "Entrypoint": ["/bin/false"] if wrong_entrypoint else entrypoint,
            "Cmd": ["/bin/false"] if wrong_cmd else command,
            "Labels": {
            "org.opencontainers.image.revision": source,
            "org.opencontainers.image.source": subject.SOURCE,
            "org.opencontainers.image.version": subject.CANDIDATE_VERSION,
            "io.cpamp.candidate.event": event,
            "io.cpamp.candidate.role": role,
        }},
    }).encode()
    config_id = blob(config)
    manifest = json.dumps({"schemaVersion": 2, "mediaType": subject.MANIFEST_TYPE,
                           "config": {"digest": config_id, "size": len(config),
                                      "mediaType": subject.CONFIG_TYPE},
                           "layers": [{"digest": layer_id, "size": len(layer),
                                       "mediaType": subject.LAYER_GZIP}]}).encode()
    manifest_id = blob(manifest)
    index = json.dumps({"schemaVersion": 2, "manifests": [{
        "digest": manifest_id,
        "size": len(manifest) + (1 if bad_descriptor_size else 0),
        "mediaType": "application/invalid" if bad_descriptor_type else subject.MANIFEST_TYPE,
        "platform": {"os": "linux", "architecture": arch},
    }]}).encode()
    items = {
        "oci-layout": b'{"imageLayoutVersion":"1.0.0"}',
        "index.json": index,
        "blobs/sha256/" + layer_id[7:]: layer,
        "blobs/sha256/" + config_id[7:]: config,
        "blobs/sha256/" + manifest_id[7:]: manifest,
    }
    if corrupt:
        items["blobs/sha256/" + layer_id[7:]] = b"not the declared layer"
    destination.write_bytes(tar_content(items))


class EmbeddedCandidateContracts(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        # Real, offline Go ELF fixtures: synthetic 64-byte ELF headers must
        # never be accepted as a valid CPAMP binary. No CPA upstream code used.
        cls.go_fixture_tmp = tempfile.TemporaryDirectory(prefix="cpamp-go-binary-fixture-")
        cls.addClassCleanup(cls.go_fixture_tmp.cleanup)
        base = Path(cls.go_fixture_tmp.name)
        for key in ("launcher", "manager", "manager_wrong_stamp", "manager_sha_suffix",
                    "manager_version_suffix", "ingress", "runtime-supervisor"):
            role = "manager" if key.startswith("manager") else key
            module, command = subject.GO_MODULES[role]
            work = base / key
            (work / command).mkdir(parents=True)
            (work / "go.mod").write_text("module " + module + "\n\ngo 1.20\n", encoding="utf-8")
            source = "package main\nfunc main() {}\n"
            if role == "manager":
                buildinfo = work / "internal/buildinfo"
                buildinfo.mkdir(parents=True)
                (buildinfo / "buildinfo.go").write_text(
                    'package buildinfo\nvar Version = "dev"\nvar SourceCommit = "unknown"\n',
                    encoding="utf-8",
                )
                source = ('package main\nimport ("fmt"; "' + module + '/internal/buildinfo")\n'
                          'func main() { fmt.Println(buildinfo.Version, buildinfo.SourceCommit) }\n')
            (work / command / "main.go").write_text(source, encoding="utf-8")
            env = dict(os.environ, GOTOOLCHAIN="local", GOPROXY="off", GOSUMDB="off",
                       GOOS="linux", GOARCH="amd64", CGO_ENABLED="0")
            args = ["go", "build", "-o", str(work / "candidate")]
            if role != "manager":
                args.insert(2, "-trimpath")
            # Go omits -ldflags metadata when -trimpath is enabled. The actual
            # Manager Docker build keeps the non-trimpath linker stamp.
            if role == "manager":
                stamp = "b" * 40 if key == "manager_wrong_stamp" else SHA
                if key == "manager_sha_suffix":
                    stamp += "extra"
                version = subject.CANDIDATE_VERSION
                if key == "manager_version_suffix":
                    version += "-modified"
                args += ["-ldflags",
                         "-X " + module + "/internal/buildinfo.Version=" +
                         version +
                         " -X " + module + "/internal/buildinfo.SourceCommit=" + stamp]
            args += ["./" + command]
            completed = subprocess.run(args, cwd=work, env=env, capture_output=True,
                                       text=True, timeout=75)
            if completed.returncode != 0:
                raise AssertionError("cannot create local Go fixture: " + completed.stderr)
            ROLE_BINARIES[key] = (work / "candidate").read_bytes()
        ROLE_BINARIES["runtime"] = ROLE_BINARIES["runtime-supervisor"]

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.license = b"MIT License\n"
        self.file = self.root / "candidate.tar"

    def inspect(self, role="slim", unpinned_cpa=False, **kwargs):
        make_oci(self.file, role, self.license, **kwargs)
        if unpinned_cpa:
            return subject.inspect(self.file, role, SHA, EVENT, self.license, b"seed")
        fake_cpa = hashlib.sha256(ROLE_BINARIES["ingress"]).hexdigest()
        fake_license = hashlib.sha256(b"official").hexdigest()
        with patch.object(subject, "CPA_SHA", fake_cpa), patch.object(
                subject, "CPA_LICENSE_SHA", fake_license):
            return subject.inspect(self.file, role, SHA, EVENT, self.license, b"seed")

    def test_two_variant_structural_receipt_with_mocked_official_fixture(self):
        # Only this fixture mocks CPA official hashes; production verifier uses immutable pins.
        (self.root / "LICENSE").write_bytes(self.license)
        seed = self.root / "docker/runtime/config.v8.default.seed.yaml"
        seed.parent.mkdir(parents=True)
        seed.write_bytes(b"seed")
        inventory = self.root / "bin/release/embedded-third-party-inventory.txt"
        inventory.parent.mkdir(parents=True)
        inventory.write_bytes(SOURCE_BYTES)
        api = self.root / "tests/fixtures/r1-embedded-api-transport.json"
        api.parent.mkdir(parents=True)
        api.write_bytes(MOCK_API_CONTRACT)
        fake_cpa = hashlib.sha256(ROLE_BINARIES["ingress"]).hexdigest()
        fake_license = hashlib.sha256(b"official").hexdigest()
        with patch.object(subject, "CPA_SHA", fake_cpa), patch.object(
                subject, "CPA_LICENSE_SHA", fake_license):
            archives = {}
            for role in subject.ROLES:
                archive = self.root / (role + ".tar")
                make_oci(archive, role, self.license)
                archives[role] = archive
            receipt = subject.verify(archives, SHA, EVENT, self.root)
            self.assertEqual(receipt["status"],
                             "STRUCTURAL_CANDIDATE_EVIDENCE_ONLY_NOT_RELEASE_QUALIFIED")
            self.assertEqual(set(receipt["images"]), set(subject.ROLES))
            self.assertEqual(len({x["platformManifestDigest"] for x in receipt["images"].values()}), 2)
            self.assertEqual(set(receipt["licenseCoverage"]), set(subject.ROLES))
            self.assertEqual(receipt["licenseCoveragePolicy"]["alpineBasePackages"],
                             subject.image_evidence.ALPINE_POLICY)
            self.assertFalse(receipt["images"]["slim"]["cpaBundled"])
            self.assertTrue(receipt["images"]["full"]["cpaBundled"])
            for role, record in receipt["images"].items():
                base = record["licenseCoverage"]["base-image"]
                self.assertEqual((base["metadataOnlyPackages"], base["packagedNoticePackages"]),
                                 (1, 0), role)
                glibc = record["licenseCoverage"]["glibc-runtime"]
                self.assertEqual(glibc["policy"], "debian-copyright-files-fail-closed")
                self.assertEqual(glibc["packages"], 1)
            make_oci(archives["slim"], "slim", self.license, event="wrong-event")
            with self.assertRaisesRegex(ValueError, "build event"):
                subject.verify(archives, SHA, EVENT, self.root)
            make_oci(archives["slim"], "slim", self.license, source_inventory=SOURCE_BYTES + b" ")
            with self.assertRaisesRegex(ValueError, "source dependency inventory differs"):
                subject.verify(archives, SHA, EVENT, self.root)
            make_oci(archives["slim"], "slim", self.license)
            make_oci(archives["full"], "full", self.license, api_contract=MOCK_API_CONTRACT + b" ")
            with self.assertRaisesRegex(ValueError, "public API transport contract differs"):
                subject.verify(archives, SHA, EVENT, self.root)

    def test_slim_and_full_positive_manifest_and_binary_hashes(self):
        slim_ev = self.inspect("slim")
        self.assertEqual(slim_ev["platform"], "linux/amd64")
        self.assertEqual(len(slim_ev["platformManifestDigest"]), 71)
        self.assertIn(subject.WEB_MANIFEST, slim_ev["packagedFiles"])
        self.assertNotIn("/usr/local/bin/cli-proxy-api", slim_ev["packagedFiles"])
        self.assertNotEqual(slim_ev["ociLayoutIndexDigest"],
                            slim_ev["platformManifestDigest"])
        full_ev = self.inspect("full")
        self.assertIn("/usr/local/bin/cli-proxy-api", full_ev["packagedFiles"])
        self.assertIn("/usr/local/bin/cpamp-ingress", full_ev["packagedFiles"])

    def test_receipt_records_explicit_alpine_metadata_coverage(self):
        receipt = self.inspect("slim")
        base = receipt["licenseCoverage"]["base-image"]
        self.assertEqual(base["policy"], subject.image_evidence.ALPINE_POLICY)
        self.assertEqual(base["originalTexts"], "available-packaged-notices-only")
        self.assertEqual(base["metadataOnlyPackages"], 1)
        self.assertEqual(base["packagedNoticePackages"], 0)
        self.assertEqual(receipt["licenseCoverage"]["ingress"]["policy"],
                         subject.image_evidence.ORIGINAL_TEXT_POLICY)
    def test_receipt_rejects_false_alpine_packaged_notice_claim(self):
        def falsify(files):
            path = subject.image_evidence.notice_path("base-image", "inventory.json")
            inventory = json.loads(files[path])
            inventory["dependencies"][0]["evidence"] = subject.image_evidence.ALPINE_PACKAGED
            files[path] = subject.image_evidence.encoded(inventory)
            aggregate = json.loads(files[subject.NOTICE])
            aggregate["components"]["base-image"] = {
                "inventory": inventory, "inventorySHA256": subject.image_evidence.digest(files[path])}
            files[subject.NOTICE] = subject.image_evidence.encoded(aggregate)
            # Rebind provenance to the rewritten aggregate inventory so only the
            # false packaged-notice claim can be responsible for the rejection.
            provenance = json.loads(files[subject.image_evidence.PROVENANCE])
            provenance["licenseInventorySHA256"] = subject.image_evidence.digest(files[subject.NOTICE])
            files[subject.image_evidence.PROVENANCE] = subject.image_evidence.encoded(provenance)
            return files

        with self.assertRaisesRegex(ValueError, "invalid installed Alpine license evidence"):
            self.inspect("slim", mutate_evidence=falsify)

    @staticmethod
    def rewrite_component_inventory(files, component_name, change, cpa_bundled=False):
        """Rewrite a component inventory and recompute every dependent digest.

        Provenance is re-validated here, so only a semantic rule may reject the
        resulting image: no hash, summary, or identity check can be responsible.
        """
        image_evidence = subject.image_evidence
        path = image_evidence.notice_path(component_name, "inventory.json")
        inventory = json.loads(files[path])
        change(inventory)
        files[path] = image_evidence.encoded(inventory)
        aggregate = json.loads(files[subject.NOTICE])
        component = aggregate["components"][component_name]
        component["inventory"] = inventory
        component["inventorySHA256"] = image_evidence.digest(files[path])
        aggregate["licenseCoverage"] = image_evidence.license_coverage(aggregate["components"])
        files[subject.NOTICE] = image_evidence.encoded(aggregate)
        provenance = json.loads(files[image_evidence.PROVENANCE])
        provenance["licenseInventorySHA256"] = image_evidence.digest(files[subject.NOTICE])
        provenance["licenseCoverage"] = aggregate["licenseCoverage"]
        files[image_evidence.PROVENANCE] = image_evidence.encoded(provenance)
        image_evidence.validate_provenance(provenance, "cpamp", subject.CANDIDATE_VERSION, SHA,
                                           files.__getitem__, cpa_bundled=cpa_bundled)
        return files

    def test_receipt_rejects_glibc_runtime_downgraded_to_alpine(self):
        def reclassify(files):
            def change(value):
                package = {"name": "musl", "version": "1.2.5-r1", "licenseExpression": "MIT",
                           "evidence": subject.image_evidence.ALPINE_METADATA_ONLY, "notices": []}
                value.update(ecosystem="alpine", dependencies=[package],
                             coveragePolicy=subject.image_evidence.ALPINE_POLICY,
                             coverage=subject.image_evidence.alpine_coverage([package]))
            return self.rewrite_component_inventory(files, "glibc-runtime", change, cpa_bundled=True)

        with self.assertRaisesRegex(ValueError, "glibc-runtime ecosystem must be debian"):
            self.inspect("full", mutate_evidence=reclassify)

    def test_receipt_rejects_alpine_package_reusing_another_package_notice(self):
        data = b"original busybox license\n"
        shared = "packaged-notices/busybox/COPYING"

        def cross_reference(files):
            files[subject.image_evidence.notice_path("base-image", shared)] = data

            def change(value):
                packages = [
                    {"name": "musl", "version": "1.2.5-r9", "licenseExpression": "MIT",
                     "evidence": subject.image_evidence.ALPINE_PACKAGED,
                     "notices": [{"path": shared, "sha256": subject.image_evidence.digest(data)}]},
                    {"name": "busybox", "version": "1.37-r1", "licenseExpression": "GPL-2.0-only",
                     "evidence": subject.image_evidence.ALPINE_PACKAGED,
                     "notices": [{"path": shared, "sha256": subject.image_evidence.digest(data)}]},
                ]
                value.update(dependencies=packages,
                             coveragePolicy=subject.image_evidence.ALPINE_POLICY,
                             coverage=subject.image_evidence.alpine_coverage(packages))
            return self.rewrite_component_inventory(files, "base-image", change, cpa_bundled=False)

        with self.assertRaisesRegex(ValueError, "does not belong to its package"):
            self.inspect("slim", mutate_evidence=cross_reference)
    def test_reject_invalid_descriptor_size(self):
        with self.assertRaisesRegex(ValueError, "descriptor size"):
            self.inspect(bad_descriptor_size=True)

    def test_reject_invalid_descriptor_type(self):
        with self.assertRaisesRegex(ValueError, "descriptor mediaType"):
            self.inspect(bad_descriptor_type=True)

    def test_reject_invalid_rootfs_diff_id(self):
        with self.assertRaisesRegex(ValueError, "rootfs diff_id"):
            self.inspect(bad_diff_id=True)

    def test_reject_missing_api_transport_fixture(self):
        with self.assertRaisesRegex(ValueError, "missing packaged files"):
            self.inspect(missing=subject.API_CONTRACT)

    def test_reject_mismatched_api_contract(self):
        with self.assertRaisesRegex(ValueError, "Web/API transport contract"):
            self.inspect(api_contract=b"{}")

    def test_reject_non_executable_manager(self):
        with self.assertRaisesRegex(ValueError, "not safely executable"):
            self.inspect(bad_mode=subject.GO_BINARIES["manager"])

    def test_reject_non_executable_entrypoint(self):
        with self.assertRaisesRegex(ValueError, "not safely executable"):
            self.inspect("full", bad_mode="/usr/local/bin/cpamp-launcher")
    def test_reject_fake_elf_header_without_loadable_segments(self):
        with self.assertRaisesRegex(ValueError, "invalid ELF program headers"):
            self.inspect(invalid_elf=True)

    def test_reject_wrong_compiled_go_module(self):
        with self.assertRaisesRegex(ValueError, "Go compiled module/entrypoint mismatch"):
            self.inspect(wrong_module=True)

    def test_reject_wrong_manager_compiled_source_stamp(self):
        with self.assertRaisesRegex(ValueError, "Manager compiled source/version stamp mismatch"):
            self.inspect(wrong_source_stamp=True)

    def test_reject_wrong_startup_entrypoint_on_each_role(self):
        for role in subject.ROLES:
            with self.subTest(role=role):
                with self.assertRaisesRegex(ValueError, "Entrypoint/Cmd mismatch"):
                    self.inspect(role, wrong_entrypoint=True)

    def test_reject_wrong_startup_command_on_each_role(self):
        for role in subject.ROLES:
            with self.subTest(role=role):
                with self.assertRaisesRegex(ValueError, "Entrypoint/Cmd mismatch"):
                    self.inspect(role, wrong_cmd=True)

    def test_reject_runtime_seed_config_mismatch(self):
        make_oci(self.file, "full", self.license)
        fake_cpa = hashlib.sha256(ROLE_BINARIES["ingress"]).hexdigest()
        fake_license = hashlib.sha256(b"official").hexdigest()
        with patch.object(subject, "CPA_SHA", fake_cpa), patch.object(
                subject, "CPA_LICENSE_SHA", fake_license):
            with self.assertRaisesRegex(ValueError, "official v8 default seed mismatch"):
                subject.inspect(self.file, "full", SHA, EVENT, self.license, b"tampered-seed")
    def test_reject_manager_source_commit_with_correct_prefix(self):
        with self.assertRaisesRegex(ValueError, "Manager compiled source/version stamp mismatch"):
            self.inspect(wrong_sha_suffix=True)

    def test_reject_manager_version_with_correct_prefix(self):
        with self.assertRaisesRegex(ValueError, "Manager compiled source/version stamp mismatch"):
            self.inspect(wrong_version_suffix=True)

    def test_reject_modified_oci_blob(self):
        with self.assertRaisesRegex(ValueError, "digest mismatch"):
            self.inspect(corrupt=True)

    def test_reject_wrong_source(self):
        with self.assertRaisesRegex(ValueError, "source identity"):
            self.inspect(source="b" * 40)

    def test_reject_wrong_event(self):
        with self.assertRaisesRegex(ValueError, "build event"):
            self.inspect(event="different-event")

    def test_reject_wrong_platform(self):
        with self.assertRaisesRegex(ValueError, "linux/amd64"):
            self.inspect(arch="arm64")

    def test_reject_missing_web_evidence(self):
        with self.assertRaisesRegex(ValueError, "missing packaged files"):
            self.inspect(missing=subject.WEB_MANIFEST)

    def test_reject_missing_notice_inventory(self):
        with self.assertRaisesRegex(ValueError, "missing packaged files"):
            self.inspect("slim", missing=subject.NOTICE)

    def test_reject_full_with_unpinned_cpa(self):
        with self.assertRaisesRegex(ValueError, "official CPA"):
            self.inspect("full", unpinned_cpa=True)

    def test_reject_slim_bundling_cpa_binary(self):
        with self.assertRaisesRegex(ValueError, "unexpected packaged files"):
            self.inspect("slim", mutate_evidence=lambda files: {
                **files,
                "/usr/local/bin/cli-proxy-api": ROLE_BINARIES["ingress"],
            })

    def test_verify_rendered_compose_and_pairing_against_oci_receipt(self):
        (self.root / "LICENSE").write_bytes(self.license)
        seed = self.root / "docker/runtime/config.v8.default.seed.yaml"
        seed.parent.mkdir(parents=True)
        seed.write_bytes(b"seed")
        entrypoint = self.root / "docker/runtime/v8-entrypoint.sh"
        entrypoint.write_bytes(MOCK_RUNTIME_ENTRYPOINT)
        inventory = self.root / "bin/release/embedded-third-party-inventory.txt"
        inventory.parent.mkdir(parents=True)
        inventory.write_bytes(SOURCE_BYTES)
        real_api_bytes = (VERIFIER.parents[2] / "tests/fixtures/r1-embedded-api-transport.json").read_bytes()
        api = self.root / "tests/fixtures/r1-embedded-api-transport.json"
        api.parent.mkdir(parents=True)
        api.write_bytes(real_api_bytes)
        fake_cpa = hashlib.sha256(ROLE_BINARIES["ingress"]).hexdigest()
        fake_license = hashlib.sha256(b"official").hexdigest()
        renderer = VERIFIER.parent / "render-embedded-compose.mjs"
        with patch.object(subject, "CPA_SHA", fake_cpa), patch.object(
                subject, "CPA_LICENSE_SHA", fake_license):
            archives = {}
            for role in subject.ROLES:
                archive = self.root / (role + ".oci.tar")
                make_oci(archive, role, self.license, api_contract=real_api_bytes)
                archives[role] = archive
            receipt = subject.verify(archives, SHA, EVENT, self.root)
            real_cpa_sha = VERIFIER.read_text(encoding="utf-8").split(
                'CPA_SHA = "', 1)[1].split('"', 1)[0]
            evidence_path = self.root / "candidate-evidence.json"
            compose_path = self.root / "docker-compose.v2-embedded.yml"
            pairing_path = self.root / "embedded-release-pairing.json"

            # Production surface contract: neither Python verifier nor Node renderer exposes any
            # pin-override / test-setter attribute or export.
            py_forbidden = [
                name for name in dir(subject)
                if any(tok in name.lower() for tok in ("fortest", "for_test", "setexpected", "set_expected", "activeexpected", "active_expected", "official_cpa_sha"))
            ]
            self.assertEqual(py_forbidden, [])
            # Exact allowed export list for bin/release/render-embedded-compose.mjs
            # (kept in sync with tests/embeddedCandidate.test.mjs), plus process.env Proxy probe.
            expected_node_exports = [
                "CANONICAL_INGRESS_PORT_SPEC",
                "CLI_HELP_TEXT",
                "COMPOSE_FILENAME",
                "COMPOSE_SERVICES",
                "DEFAULT_REGISTRY_PREFIX",
                "DEFAULT_REPOSITORIES",
                "EMBEDDED_VERIFIER_CONTRACT_NOTICE",
                "EXPECTED_API_CONTRACT_ID",
                "EXPECTED_API_CONTRACT_QUALIFICATION",
                "EXPECTED_API_PATH",
                "EXPECTED_API_SUFFIX",
                "EXPECTED_CPA_ARTIFACT_ID",
                "EXPECTED_CPA_SHA256",
                "EXPECTED_CPA_VERSION",
                "PACKAGED_PATHS",
                "PAIRING_FILENAME",
                "ROLES",
                "assertCanonicalCpaArtifact",
                "composeYamlToRuntime12ConfigJson",
                "extractFilesFromOciArchive",
                "parseEmbeddedComposeText",
                "parsePinnedImageReference",
                "renderEmbeddedComposeYaml",
                "renderEmbeddedReleaseArtifacts",
                "resolveFullWebAndApiMetadata",
                "runCli",
                "validateCandidateEvidence",
                "validateRegistryPrefix",
                "validateRuntime12ComposeDocument",
                "validateVersionTag",
                "verifyEmbeddedReleaseArtifacts",
                "verifyOciArchiveAgainstRoleEvidence",
            ]
            node_probe_proc = subprocess.run(
                [
                    "node", "--input-type=module", "-e",
                    "const allowed = new Set(['NODE_PATH']);\n"
                    "const desc = Object.getOwnPropertyDescriptor(process, 'env');\n"
                    f"const fake = {json.dumps(fake_cpa)};\n"
                    "Object.defineProperty(process, 'env', {\n"
                    "  ...desc,\n"
                    "  value: new Proxy(process.env, {\n"
                    "    get(t, p, r) { return typeof p === 'string' && !allowed.has(p) ? fake : Reflect.get(t, p, r); }\n"
                    "  }),\n"
                    "});\n"
                    f"const m = await import({json.dumps(renderer.resolve().as_uri() + '?env-probe=1')});\n"
                    "Object.defineProperty(process, 'env', desc);\n"
                    f"const syntheticEv = {json.dumps(receipt)};\n"
                    "let rejected = false;\n"
                    "try { m.validateCandidateEvidence(syntheticEv); } catch (e) {\n"
                    "  rejected = /officialCPA pin mismatch/.test(String(e));\n"
                    "}\n"
                    "console.log(JSON.stringify({ exports: Object.keys(m).sort(), rejected }));\n",
                ],
                capture_output=True, text=True, timeout=20, check=True,
            )
            node_probe = json.loads(node_probe_proc.stdout)
            self.assertEqual(node_probe["exports"], expected_node_exports)
            self.assertTrue(
                node_probe["rejected"],
                "production renderer accepted non-official CPA pin when process.env returned fixture sha",
            )
            self.assertEqual(
                real_cpa_sha,
                "e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7",
            )

            # Round-4 P1 regression: unpatched CLI renderer must reject full.oci.tar whose CPA executable
            # bytes (fake_cpa) do not match the official v8.0.55 pin even if cpa-artifact.json is self-consistent
            mismatch_ev = json.loads(json.dumps(receipt))
            mismatch_ev["officialCPA"]["verifiedFinalImageExecutableSHA256"] = real_cpa_sha
            mismatch_ev["images"]["full"]["packagedFiles"]["/usr/local/bin/cli-proxy-api"] = (
                "sha256:" + real_cpa_sha
            )
            evidence_path.write_text(json.dumps(mismatch_ev, indent=2) + "\n", encoding="utf-8")
            mismatch_render = subprocess.run(
                ["node", str(renderer), "--evidence", str(evidence_path),
                 "--compose-out", str(compose_path), "--pairing-out", str(pairing_path)],
                capture_output=True, text=True, timeout=20, check=False)
            self.assertNotEqual(mismatch_render.returncode, 0)
            self.assertIn(
                "OCI archive official CPA v8.0.55 executable hash mismatch for full",
                mismatch_render.stderr,
            )

            # Positive synthetic render uses an isolated test module without public runCli.
            receipt_for_node = json.loads(json.dumps(receipt))
            evidence_path.write_text(json.dumps(receipt_for_node, indent=2) + "\n", encoding="utf-8")
            _, render_runner = stage_renderer(self.root / "synthetic-fixture", fake_cpa)
            rendered = subprocess.run(
                ["node", str(render_runner), "--evidence", str(evidence_path),
                 "--compose-out", str(compose_path), "--pairing-out", str(pairing_path)],
                cwd=str(VERIFIER.parents[2]),
                capture_output=True, text=True, timeout=20, check=False)
            self.assertEqual(rendered.returncode, 0, rendered.stdout + "\n" + rendered.stderr)

            # Round-4 P1 regression: unpatched CLI --verify and Python CLI must also reject the official-pin mismatch
            mismatch_pairing = json.loads(pairing_path.read_text(encoding="utf-8"))
            mismatch_pairing["cpa"]["artifact_id"] = "sha256:" + real_cpa_sha
            mismatch_ev_path = self.root / "mismatch-evidence.json"
            mismatch_pairing_path = self.root / "mismatch-pairing.json"
            mismatch_ev_path.write_text(json.dumps(mismatch_ev, indent=2) + "\n", encoding="utf-8")
            mismatch_pairing_path.write_text(json.dumps(mismatch_pairing, indent=2) + "\n", encoding="utf-8")
            mismatch_verify_node = subprocess.run(
                ["node", str(renderer), "--verify",
                 "--evidence", str(mismatch_ev_path),
                 "--compose", str(compose_path),
                 "--pairing", str(mismatch_pairing_path),
                 "--slim-oci", str(archives["slim"]),
                 "--full-oci", str(archives["full"])],
                capture_output=True, text=True, timeout=20, check=False)
            self.assertNotEqual(mismatch_verify_node.returncode, 0)
            self.assertIn(
                "OCI archive official CPA v8.0.55 executable hash mismatch for full",
                mismatch_verify_node.stderr,
            )
            mismatch_verify_py = subprocess.run(
                [sys.executable, str(VERIFIER),
                 "--evidence", str(mismatch_ev_path),
                 "--verify-compose", str(compose_path),
                 "--verify-pairing", str(mismatch_pairing_path),
                 "--slim", str(archives["slim"]),
                 "--full", str(archives["full"])],
                capture_output=True, text=True, timeout=20, check=False)
            self.assertNotEqual(mismatch_verify_py.returncode, 0)
            self.assertIn(
                "OCI archive official CPA v8.0.55 executable hash mismatch",
                mismatch_verify_py.stderr,
            )
            test_archives = dict(archives)
            with redirect_fixture_node(subject, render_runner):
                verified = subject.verify_rendered_pairing(
                    receipt_for_node, compose_path, pairing_path, test_archives)
            self.assertEqual(verified["compose"]["filename"], "docker-compose.v2-embedded.yml")

            # Node's documented archive/topology checks do not certify Go metadata.
            # Python certification must reject a role binary from a different Go module,
            # even when its archive hashes, receipt, compose and pairing all agree.
            valid_compose = compose_path.read_bytes()
            valid_pairing = pairing_path.read_bytes()
            for role in subject.ROLES:
                archive = archives[role]
                original_archive = archive.read_bytes()
                try:
                    wrong_binary = ROLE_BINARIES["runtime-supervisor"]
                    binary_path = subject.GO_BINARIES["ingress"]
                    make_oci(
                        archive, role, self.license, api_contract=real_api_bytes,
                        mutate_evidence=lambda files: {**files, binary_path: wrong_binary},
                    )
                    wrong_identity = json.loads(json.dumps(receipt_for_node))
                    image = wrong_identity["images"][role]
                    with tarfile.open(archive) as tf:
                        index = json.load(tf.extractfile("index.json"))
                        image["ociLayoutIndexDigest"] = blob(tf.extractfile("index.json").read())
                        manifest_digest = index["manifests"][0]["digest"]
                        manifest = json.load(tf.extractfile("blobs/" + manifest_digest.replace(":", "/")))
                    image["archiveSHA256"] = blob(archive.read_bytes())
                    image["platformManifestDigest"] = manifest_digest
                    image["imageConfigDigest"] = manifest["config"]["digest"]
                    image["packagedFiles"][binary_path] = blob(wrong_binary)
                    evidence_path.write_text(json.dumps(wrong_identity))
                    rendered_wrong = subprocess.run(
                        ["node", str(render_runner), "--evidence", str(evidence_path),
                         "--compose-out", str(compose_path), "--pairing-out", str(pairing_path)],
                        cwd=str(VERIFIER.parents[2]),
                        capture_output=True, text=True, timeout=20, check=False)
                    self.assertEqual(rendered_wrong.returncode, 0, rendered_wrong.stderr)
                    with self.assertRaisesRegex(ValueError, "Go compiled module/entrypoint mismatch: ingress"):
                        subject.verify_rendered_pairing(
                            wrong_identity, compose_path, pairing_path, test_archives)
                finally:
                    archive.write_bytes(original_archive)
                    compose_path.write_bytes(valid_compose)
                    pairing_path.write_bytes(valid_pairing)
                    evidence_path.write_text(json.dumps(receipt_for_node))

            # Round-4 P1 regression: tampered cpa-artifact.json digest in evidence fails closed
            bad_cpa_art_ev = json.loads(json.dumps(receipt_for_node))
            bad_cpa_art_ev["images"]["full"]["packagedFiles"][
                "/usr/local/share/cpamp/runtime/cpa-artifact.json"
            ] = "sha256:" + "7" * 64
            with self.assertRaisesRegex(
                ValueError, "OCI archive CPA artifact manifest digest mismatch against evidence"
            ):
                subject.verify_rendered_pairing(
                    bad_cpa_art_ev, compose_path, pairing_path, test_archives)
            # Round-3 regression: missing any single variant archive or archive=None/""/wrong fails closed
            with self.assertRaisesRegex(ValueError, "missing required OCI archive for full"):
                subject.verify_rendered_pairing(
                    receipt_for_node, compose_path, pairing_path,
                    {"slim": archives["slim"], "full": None})
            for bad_archive_val in (None, "", "wrong.tar"):
                bad_ev_arch = json.loads(json.dumps(receipt_for_node))
                bad_ev_arch["images"]["full"]["archive"] = bad_archive_val
                with self.assertRaisesRegex(ValueError, "invalid or missing archive filename for full"):
                    subject.verify_rendered_pairing(
                        bad_ev_arch, compose_path, pairing_path, test_archives)

            with redirect_fixture_node(subject, render_runner):
                # Negative 1: :latest in compose
                original_compose = compose_path.read_text(encoding="utf-8")
                compose_path.write_text(
                    original_compose.replace(
                        "@" + receipt_for_node["images"]["full"]["platformManifestDigest"],
                        ":latest"),
                    encoding="utf-8")
                with self.assertRaisesRegex(ValueError, "must not reference :latest"):
                    subject.verify_rendered_pairing(
                        receipt_for_node, compose_path, pairing_path, test_archives)

                # Negative 2: build block in compose
                compose_path.write_text(
                    original_compose + "    build:\n      context: .\n",
                    encoding="utf-8")
                with self.assertRaisesRegex(ValueError, "must not contain local build blocks"):
                    subject.verify_rendered_pairing(
                        receipt_for_node, compose_path, pairing_path, test_archives)

                # Negative 3: loopback env in compose
                compose_path.write_text(
                    original_compose.replace(
                        "      CPAMP_DRAIN_TIMEOUT: '${CPAMP_DRAIN_TIMEOUT:-90s}'",
                        "      CPAMP_DRAIN_TIMEOUT: '${CPAMP_DRAIN_TIMEOUT:-90s}'\n      CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK: '1'"),
                    encoding="utf-8")
                with self.assertRaisesRegex(ValueError, "CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK"):
                    subject.verify_rendered_pairing(
                        receipt_for_node, compose_path, pairing_path, test_archives)

                compose_path.write_text(original_compose, encoding="utf-8")

                # Negative 4: tampered pairing digest
                original_pairing = json.loads(pairing_path.read_text(encoding="utf-8"))
                bad_pairing = json.loads(json.dumps(original_pairing))
                bad_pairing["images"]["full"]["manifest_digest"] = "sha256:" + "f" * 64
                pairing_path.write_text(json.dumps(bad_pairing), encoding="utf-8")
                with self.assertRaisesRegex(ValueError, "pairing manifest_digest mismatch"):
                    subject.verify_rendered_pairing(
                        receipt_for_node, compose_path, pairing_path, test_archives)

                # Helper to write a mutated compose and re-sync pairing.compose digest/size
                def write_resynced_compose(mutated_compose_text):
                    raw_bytes = mutated_compose_text.encode("utf-8")
                    compose_path.write_bytes(raw_bytes)
                    synced = json.loads(json.dumps(original_pairing))
                    synced["compose"]["sha256"] = blob(raw_bytes)
                    synced["compose"]["size"] = len(raw_bytes)
                    pairing_path.write_text(json.dumps(synced), encoding="utf-8")

                # Negative 5: extra port on cpa-manager-plus (normal and deeper-indented YAML)
                for port_block in (
                    "    ports:\n      - '${CPAMP_PUBLIC_PORT:-18317}:18317'\n      - '18318:18317'",
                    "    ports:\n        - '${CPAMP_PUBLIC_PORT:-18317}:18317'\n        - '18318:18317'",
                ):
                    write_resynced_compose(
                        original_compose.replace(
                            "    ports:\n      - '${CPAMP_PUBLIC_PORT:-18317}:18317'", port_block
                        )
                    )
                    with self.assertRaisesRegex(
                        ValueError, "expected only cpa-manager-plus 18317:18317"
                    ):
                        subject.verify_rendered_pairing(
                            receipt_for_node, compose_path, pairing_path, test_archives)

                # Negative 6: privileged: true / "true", network_mode: host, read-only /data, cross-role mount
                write_resynced_compose(
                    original_compose.replace(
                        "  cpa-manager-plus:\n", "  cpa-manager-plus:\n    privileged: true\n"
                    )
                )
                with self.assertRaisesRegex(ValueError, "cpa-manager-plus must not run privileged"):
                    subject.verify_rendered_pairing(
                        receipt_for_node, compose_path, pairing_path, test_archives)

                write_resynced_compose(
                    original_compose.replace(
                        "  cpa-manager-plus:\n", '  cpa-manager-plus:\n    privileged: "true"\n'
                    )
                )
                with self.assertRaisesRegex(ValueError, "cpa-manager-plus.privileged must be a boolean"):
                    subject.verify_rendered_pairing(
                        receipt_for_node, compose_path, pairing_path, test_archives)

                write_resynced_compose(
                    original_compose.replace(
                        "  cpa-manager-plus:\n", "  cpa-manager-plus:\n    network_mode: host\n"
                    )
                )
                with self.assertRaisesRegex(
                    ValueError, "cpa-manager-plus must not use host networking"
                ):
                    subject.verify_rendered_pairing(
                        receipt_for_node, compose_path, pairing_path, test_archives)

                write_resynced_compose(
                    original_compose.replace(
                        "- cpamp-data:/data", "- cpamp-data:/data:ro"
                    )
                )
                with self.assertRaisesRegex(
                    ValueError, "cpa-manager-plus /data volume must be writable"
                ):
                    subject.verify_rendered_pairing(
                        receipt_for_node, compose_path, pairing_path, test_archives)

                write_resynced_compose(
                    original_compose.replace(
                        "      - cpamp-data:/data",
                        "      - cpamp-data:/data\n      - cpamp-runtime-data:/runtime-copy",
                    )
                )
                with self.assertRaisesRegex(
                    ValueError, "strict volume whitelist violation on cpa-manager-plus"
                ):
                    subject.verify_rendered_pairing(
                        receipt_for_node, compose_path, pairing_path, test_archives)

                # Round-3 regression: top-level volume override (name/external/driver/driver_opts/labels)
                write_resynced_compose(
                    original_compose.replace(
                        "  cpamp-data:\n",
                        "  cpamp-data:\n    name: cpamp-shared-state\n",
                    )
                )
                with self.assertRaisesRegex(
                    ValueError, "top-level volume cpamp-data must be a canonical empty definition"
                ):
                    subject.verify_rendered_pairing(
                        receipt_for_node, compose_path, pairing_path, test_archives)
                # Restore valid compose and pairing, then test per-role sourceCommitClaim drift
                compose_path.write_text(original_compose, encoding="utf-8")
                pairing_path.write_text(json.dumps(original_pairing), encoding="utf-8")
                for role in subject.ROLES:
                    drifted = json.loads(json.dumps(receipt_for_node))
                    drifted["images"][role]["sourceCommitClaim"] = "0" * 40
                    with self.assertRaisesRegex(
                        ValueError, role + " sourceCommitClaim differs"
                    ):
                        subject.verify_rendered_pairing(
                            drifted, compose_path, pairing_path, test_archives)

                # Negative 7 (P1-1): Archives unchanged, evidence+pairing+compose consistently forged
                # Case A: consistently forged manager platformManifestDigest
                real_full_manifest = receipt_for_node["images"]["full"]["platformManifestDigest"]
                forged_full_manifest = "sha256:" + "9" * 64
                forged_ev_a = json.loads(json.dumps(receipt_for_node))
                forged_ev_a["images"]["full"]["platformManifestDigest"] = forged_full_manifest
                forged_compose_a = original_compose.replace(real_full_manifest, forged_full_manifest)
                forged_compose_a_bytes = forged_compose_a.encode("utf-8")
                compose_path.write_bytes(forged_compose_a_bytes)
                forged_pairing_a = json.loads(json.dumps(original_pairing))
                forged_pairing_a["images"]["full"]["manifest_digest"] = forged_full_manifest
                forged_pairing_a["images"]["full"]["image_ref"] = (
                    forged_pairing_a["images"]["full"]["image_ref"].replace(
                        real_full_manifest, forged_full_manifest
                    )
                )
                forged_pairing_a["compose"]["sha256"] = blob(forged_compose_a_bytes)
                forged_pairing_a["compose"]["size"] = len(forged_compose_a_bytes)
                pairing_path.write_text(json.dumps(forged_pairing_a), encoding="utf-8")
                with self.assertRaisesRegex(
                    ValueError, "OCI archive platformManifestDigest mismatch"
                ):
                    subject.verify_rendered_pairing(
                        forged_ev_a, compose_path, pairing_path, test_archives)

                # Case B: consistently forged sourceCommitClaim across evidence + pairing
                compose_path.write_text(original_compose, encoding="utf-8")
                forged_commit = "1" * 40
                forged_ev_b = json.loads(json.dumps(receipt_for_node))
                forged_ev_b["sourceCommitClaim"] = forged_commit
                for role in subject.ROLES:
                    forged_ev_b["images"][role]["goBinaryIdentity"]["managerCompiledSourceClaim"] = (
                        forged_commit
                    )
                forged_pairing_b = json.loads(json.dumps(original_pairing))
                forged_pairing_b["source_commit"] = forged_commit
                pairing_path.write_text(json.dumps(forged_pairing_b), encoding="utf-8")
                with self.assertRaisesRegex(
                    ValueError, "OCI archive revision label mismatch"
                ):
                    subject.verify_rendered_pairing(
                        forged_ev_b, compose_path, pairing_path, test_archives)

                # Case C: consistently forged fullWebSummary.indexHtmlSHA256 across evidence + pairing
                forged_index_sha = "sha256:" + "8" * 64
                forged_ev_c = json.loads(json.dumps(receipt_for_node))
                for role in subject.ROLES:
                    forged_ev_c["images"][role]["fullWebSummary"]["indexHtmlSHA256"] = forged_index_sha
                forged_pairing_c = json.loads(json.dumps(original_pairing))
                forged_pairing_c["fullweb"]["index_html_sha256"] = forged_index_sha
                pairing_path.write_text(json.dumps(forged_pairing_c), encoding="utf-8")
                with self.assertRaisesRegex(
                    ValueError, "OCI archive Full Web manifest or index.html summary mismatch"
                ):
                    subject.verify_rendered_pairing(
                        forged_ev_c, compose_path, pairing_path, test_archives)

def stage_mock_candidate_repo(source):
    repo_root = VERIFIER.parents[2]
    mock_cpa_sha = hashlib.sha256(b"mock-cpa-binary").hexdigest()
    for rel in (
        "bin/release/build-embedded-candidate.sh",
        "bin/ci/validate-runtime12-compose.mjs",
        "tests/fixtures/r1-embedded-api-transport.json",
    ):
        dst = source / rel
        dst.parent.mkdir(parents=True, exist_ok=True)
        dst.write_bytes((repo_root / rel).read_bytes())
    _, runner = stage_renderer(source / "bin/release", mock_cpa_sha)
    (source / "bin/release/render-embedded-compose.mjs").write_bytes(runner.read_bytes())
    qual = source / "tests/fixtures/cpa-v8-qualification/.keep"
    qual.parent.mkdir(parents=True, exist_ok=True)
    qual.write_bytes(b"")

MOCK_VERIFIER_SCRIPT = """\
import gzip
import hashlib
import io
import json
import pathlib
import sys
import tarfile

a = sys.argv
if "--evidence" in a and "--out" not in a:
    ev = json.loads(pathlib.Path(a[a.index("--evidence") + 1]).read_text(encoding="utf-8"))
    compose = pathlib.Path(a[a.index("--verify-compose") + 1]).read_bytes()
    pairing = json.loads(pathlib.Path(a[a.index("--verify-pairing") + 1]).read_text(encoding="utf-8"))
    assert pairing["source_commit"] == ev["sourceCommitClaim"]
    assert pairing["compose"]["sha256"] == "sha256:" + hashlib.sha256(compose).hexdigest()
    sys.exit(0)

commit = a[a.index("--source-commit") + 1] if "--source-commit" in a else "a" * 40
event = a[a.index("--build-event") + 1] if "--build-event" in a else "local-test-event"
root = pathlib.Path(a[a.index("--source-root") + 1]) if "--source-root" in a else pathlib.Path(".")
{extra_checks}
api_bytes = (root / "tests/fixtures/r1-embedded-api-transport.json").read_bytes()
api_sha = "sha256:" + hashlib.sha256(api_bytes).hexdigest()
cpa_sha = hashlib.sha256(b"mock-cpa-binary").hexdigest()

def s256(data):
    return "sha256:" + hashlib.sha256(data).hexdigest()

def build_tar_bytes(entries):
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w") as tf:
        for name, content in entries.items():
            info = tarfile.TarInfo(name)
            info.size = len(content)
            tf.addfile(info, io.BytesIO(content))
    return buf.getvalue()

images = {{}}
for idx, role in enumerate(("slim", "full"), start=1):
    archive = pathlib.Path(a[a.index("--" + role) + 1])
    cpa_bundled = role == "full"
    prov_bytes = json.dumps({{"sourceCommit": commit, "role": "cpamp", "cpaBundled": cpa_bundled}}).encode("utf-8")
    idx_sha = "sha256:" + "c" * 64
    web_bytes = json.dumps({{
        "schemaVersion": 1,
        "indexHtmlSHA256": idx_sha,
        "assetCount": 2,
        "files": [
            {{"path": "index.html", "sha256": idx_sha, "size": 10}},
            {{"path": "assets/app.js", "sha256": "sha256:" + "d" * 64, "size": 20}},
        ],
    }}).encode("utf-8")
    web_sha = s256(web_bytes)
    cpa_bytes = b"mock-cpa-binary"
    cpa_art_bytes = json.dumps({{
        "schemaVersion": 1,
        "engine": "cpa",
        "version": "8.0.55",
        "artifactId": s256(cpa_bytes),
    }}).encode("utf-8")
    seed_bytes = b"seed"
    layer_entries = {{
        "usr/local/bin/cpamp-launcher": b"mock-bin-launcher",
        "usr/local/bin/cpa-manager-plus": b"mock-bin-manager",
        "usr/local/bin/cpamp-ingress": b"mock-bin-ingress",
        "usr/local/bin/cpamp-runtime-supervisor": b"mock-bin-runtime-supervisor",
        "usr/local/share/cpamp/provenance.json": prov_bytes,
        "usr/local/share/cpamp/release/fullweb-manifest.json": web_bytes,
        "usr/local/share/cpamp/release/public-api-transport-contract.json": api_bytes,
        "usr/local/share/cpamp/runtime/cpa-artifact.json": cpa_art_bytes,
        "usr/local/share/cpamp/runtime/config.seed.yaml": seed_bytes,
    }}
    packaged = {{
        "/usr/local/bin/cpamp-launcher": s256(b"mock-bin-launcher"),
        "/usr/local/bin/cpa-manager-plus": s256(b"mock-bin-manager"),
        "/usr/local/bin/cpamp-ingress": s256(b"mock-bin-ingress"),
        "/usr/local/bin/cpamp-runtime-supervisor": s256(b"mock-bin-runtime-supervisor"),
        "/usr/local/share/cpamp/provenance.json": s256(prov_bytes),
        "/usr/local/share/cpamp/release/fullweb-manifest.json": web_sha,
        "/usr/local/share/cpamp/release/public-api-transport-contract.json": api_sha,
        "/usr/local/share/cpamp/runtime/cpa-artifact.json": s256(cpa_art_bytes),
        "/usr/local/share/cpamp/runtime/config.seed.yaml": s256(seed_bytes),
    }}
    if cpa_bundled:
        lic_bytes = b"official"
        layer_entries["usr/local/bin/cli-proxy-api"] = cpa_bytes
        layer_entries["usr/local/share/licenses/cli-proxy-api/LICENSE"] = lic_bytes
        packaged["/usr/local/bin/cli-proxy-api"] = "sha256:" + cpa_sha
        packaged["/usr/local/share/licenses/cli-proxy-api/LICENSE"] = s256(lic_bytes)
    extra = {{
        "cpaBundled": cpa_bundled,
        "fullWebSummary": {{
            "manifestSHA256": web_sha,
            "indexHtmlSHA256": idx_sha,
            "assetCount": 2,
        }},
        "apiTransportContract": {{
            "sha256": api_sha,
            "contractId": "cpamp-r1-embedded-public-api-transport-v1",
            "qualification": "SOURCE_TRANSPORT_CONTRACT_ONLY_NOT_COMPLETE_WEB_API_COMPATIBILITY",
            "apiSuffix": "/api/v2",
            "expectedPath": "/phase4-test/api/v2/technical",
        }},
    }}
    layer_tar = build_tar_bytes(layer_entries)
    diff_id = s256(layer_tar)
    layer_gz = gzip.compress(layer_tar)
    layer_digest = s256(layer_gz)
    config_bytes = json.dumps({{
        "architecture": "amd64",
        "os": "linux",
        "config": {{
            "Labels": {{
                "org.opencontainers.image.revision": commit,
                "io.cpamp.candidate.role": role,
                "io.cpamp.candidate.event": event,
            }},
        }},
        "rootfs": {{"type": "layers", "diff_ids": [diff_id]}},
    }}).encode("utf-8")
    cfg = s256(config_bytes)
    manifest_bytes = json.dumps({{
        "schemaVersion": 2,
        "mediaType": "application/vnd.oci.image.manifest.v1+json",
        "config": {{
            "mediaType": "application/vnd.oci.image.config.v1+json",
            "digest": cfg,
            "size": len(config_bytes),
        }},
        "layers": [{{
            "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
            "digest": layer_digest,
            "size": len(layer_gz),
        }}],
    }}).encode("utf-8")
    manifest_digest = s256(manifest_bytes)
    index_bytes = json.dumps({{
        "schemaVersion": 2,
        "manifests": [{{
            "mediaType": "application/vnd.oci.image.manifest.v1+json",
            "digest": manifest_digest,
            "size": len(manifest_bytes),
            "platform": {{"os": "linux", "architecture": "amd64"}},
        }}],
    }}).encode("utf-8")
    oci_bytes = build_tar_bytes({{
        "oci-layout": b'{{"imageLayoutVersion":"1.0.0"}}',
        "index.json": index_bytes,
        "blobs/sha256/" + manifest_digest.split(":", 1)[1]: manifest_bytes,
        "blobs/sha256/" + cfg.split(":", 1)[1]: config_bytes,
        "blobs/sha256/" + layer_digest.split(":", 1)[1]: layer_gz,
    }})
    archive.write_bytes(oci_bytes)
    docker_tar = archive.parent / (role + ".docker.tar")
    if docker_tar.exists():
        transport = json.dumps([{{
            "Config": "blobs/" + cfg.replace(":", "/", 1),
            "RepoTags": ["cpamp-r1-" + role + ":candidate"],
            "Layers": ["blobs/" + layer_digest.replace(":", "/", 1)],
        }}]).encode("utf-8")
        docker_tar.write_bytes(build_tar_bytes({{"manifest.json": transport}}))
    images[role] = {{
        "archive": role + ".oci.tar",
        "archiveSHA256": s256(oci_bytes),
        "ociLayoutIndexDigest": s256(index_bytes),
        "platform": "linux/amd64",
        "platformManifestDigest": manifest_digest,
        "imageConfigDigest": cfg,
        "goBinaryIdentity": {{
            "managerCompiledSourceClaim": commit,
        }},
        "packagedFiles": packaged,
        **extra,
    }}
out = pathlib.Path(a[a.index("--out") + 1])
out.write_text(json.dumps({{
    "schemaVersion": 1,
    "platform": "linux/amd64",
    "sourceCommitClaim": commit,
    "candidateBuildEventClaim": event,
    "candidateVersionClaim": "2.0.0-r1candidate",
    "officialCPA": {{
        "version": "8.0.55",
        "verifiedFinalImageExecutableSHA256": cpa_sha,
    }},
    "images": images,
    "sourceRoot": str(root),
    "testOnly": True,
}}), encoding="utf-8")
"""


class CandidateBuilderSnapshotContracts(unittest.TestCase):
    def test_all_three_roles_use_one_locked_git_archive_even_if_worktree_changes(self):
        # A fake buildx inspects the actual --file / build-context arguments.
        # It mutates the original worktree after Manager has built. If Ingress
        # or Runtime reads from that worktree, the test fails deterministically.
        with tempfile.TemporaryDirectory(prefix="cpamp-r1-snapshot-test-") as temp:
            base = Path(temp)
            source = base / "source"
            source.mkdir()
            for name, content in {
                "Dockerfile": b"FROM scratch\n",
                "source.txt": b"original source bytes\n",
            }.items():
                (source / name).write_bytes(content)
            stage_mock_candidate_repo(source)
            script_in_source = source / "bin/release/build-embedded-candidate.sh"
            verifier = source / "bin/release/verify-embedded-candidate.py"
            verifier.write_text(MOCK_VERIFIER_SCRIPT.format(extra_checks="""\
assert (root / "source.txt").read_text() == "original source bytes\\n"
for role in ("slim", "full"):
    archive = pathlib.Path(a[a.index("--" + role) + 1])
    assert archive.read_text() == "fake OCI " + role
"""), encoding="utf-8")
            subprocess.run(["git", "init", "-q"], cwd=source, check=True)
            subprocess.run(["git", "add", "."], cwd=source, check=True)
            subprocess.run(["git", "-c", "user.name=CI Test",
                            "-c", "user.email=ci@example.invalid", "commit", "-qm",
                            "snapshot fixture"], cwd=source, check=True)
            pinned_commit = subprocess.check_output(
                ["git", "rev-parse", "HEAD"], cwd=source, text=True).strip()

            fakebin = base / "fakebin"
            fakebin.mkdir()
            docker = fakebin / "docker"
            docker.write_text("""\
#!/usr/bin/env python3
import json, os, pathlib, sys
args = sys.argv[1:]
if args == ["buildx", "version"]:
    print("buildx mocked")
    sys.exit(0)
assert args[:2] == ["buildx", "build"], args
context = pathlib.Path(args[-1])
dockerfile = pathlib.Path(args[args.index("--file") + 1])
role = [item.split("=", 1)[1] for item in args
        if item.startswith("io.cpamp.candidate.role=")][0]
event = [item.split("=", 1)[1] for item in args
         if item.startswith("io.cpamp.candidate.event=")][0]
commit = [item.split("=", 1)[1] for item in args
          if item.startswith("org.opencontainers.image.revision=")][0]
source = pathlib.Path(os.environ["FAKE_SOURCE_DIR"])
assert context.resolve() != source.resolve()
assert dockerfile.parent == context
assert dockerfile.name == "Dockerfile"
assert (context / "source.txt").read_text() == "original source bytes\\n"
assert (context / "source.txt").stat().st_mode & 0o222 == 0
assert commit == os.environ["FAKE_COMMIT"]
target = [part.removeprefix("dest=") for item in args if item.startswith("type=oci,")
          for part in item.split(",") if part.startswith("dest=")][0]
named = [item for item in args if item.startswith("type=oci,")][0]
assert "name=cpamp-r1-" + role + ":candidate" in named, named
assert "--tag" in args and "cpamp-r1-" + role + ":candidate" in args, args
transport = [part.removeprefix("dest=") for item in args if item.startswith("type=docker,")
             for part in item.split(",") if part.startswith("dest=")]
assert len(transport) == 1, args
assert pathlib.Path(transport[0]) == pathlib.Path(target).parent / (role + ".docker.tar")
pathlib.Path(target).write_text("fake OCI " + role)
with open(os.environ["FAKE_RECORD"], "a", encoding="utf-8") as log:
    log.write(json.dumps({"role": role, "context": str(context),
                          "event": event, "commit": commit}) + "\\n")
if role == "slim":
    (source / "source.txt").write_text("MUTATED AFTER SLIM BUILD\\n")
""", encoding="utf-8")
            docker.chmod(0o755)
            output = base / "candidate-output"
            record = base / "docker-record.jsonl"
            env = dict(os.environ)
            env["PATH"] = str(fakebin) + os.pathsep + env.get("PATH", "")
            env["NODE_PATH"] = str(VERIFIER.parents[2] / "node_modules")
            env["FAKE_SOURCE_DIR"] = str(source)
            env["FAKE_COMMIT"] = pinned_commit
            env["FAKE_RECORD"] = str(record)
            result = subprocess.run(
                ["bash", str(script_in_source), str(output)],
                cwd=source, env=env, capture_output=True, text=True, timeout=25)
            self.assertEqual(result.returncode, 0,
                             result.stdout + "\n" + result.stderr)
            events = [json.loads(row) for row in record.read_text().splitlines()]
            self.assertEqual([row["role"] for row in events],
                             ["slim", "full"])
            self.assertEqual({row["commit"] for row in events}, {pinned_commit})
            self.assertEqual(len({row["event"] for row in events}), 1)
            self.assertEqual(len({row["context"] for row in events}), 1)
            self.assertEqual((source / "source.txt").read_text(),
                             "MUTATED AFTER SLIM BUILD\n")
            snapshot = Path(events[0]["context"])
            self.assertFalse(snapshot.exists(), "disposable source snapshot was not removed")
            self.assertTrue((output / "candidate-evidence.json").exists())
            self.assertTrue((output / "docker-compose.v2-embedded.yml").exists())
            self.assertTrue((output / "embedded-release-pairing.json").exists())

class CandidateLoadContracts(unittest.TestCase):
    SCRIPT = VERIFIER.parent / "build-embedded-candidate.sh"

    def test_exporter_names_each_role_and_exports_one_load_transport(self):
        script = self.SCRIPT.read_text(encoding="utf-8")
        self.assertIn('type=oci,name=cpamp-r1-$role:candidate,dest=$output/$role.oci.tar', script)
        self.assertIn('type=docker,dest=$output/$role.docker.tar', script)
        self.assertIn('--tag "cpamp-r1-$role:candidate"', script)
        args_block = script[script.index("  args=("):script.index('  if [[ -n "$target" ]]')]
        self.assertIn("type=oci,name=", args_block)
        self.assertIn("type=docker,dest=", args_block)
        self.assertNotIn("--load", script)
        self.assertNotIn("--push", script)
        self.assertNotIn("skopeo", script)
        self.assertNotIn('docker load --input "$output/$role.oci.tar"', script)

    def test_load_is_opt_in_and_checks_the_verified_image_identity(self):
        script = self.SCRIPT.read_text(encoding="utf-8")
        self.assertIn('if [[ "${CPAMP_CANDIDATE_LOAD:-0}" == "1" ]]; then', script)
        self.assertIn('docker load --input "$output/$role.docker.tar"', script)
        self.assertIn(
            'actual_id="$(docker image inspect --format \'{{.Id}}\' "$reference" 2>/dev/null || true)"',
            script)
        self.assertIn("the build transport did not carry the reference", script)
        self.assertIn('"$actual_id" != "$expected_id"', script)
        self.assertIn('"images"][sys.argv[2]]["imageConfigDigest"]', script)
        self.assertIn("load-evidence.json", script)
        self.assertIn("configBlobMatchesOCI", script)
        self.assertIn("layerDigestsMatchOCI", script)

    def test_opt_in_load_reports_identity_evidence_and_rejects_drift(self):
        with tempfile.TemporaryDirectory(prefix="cpamp-r1-load-test-") as temp:
            base = Path(temp)
            source = base / "source"
            source.mkdir()
            (source / "Dockerfile").write_bytes(b"FROM scratch\n")
            (source / "source.txt").write_bytes(b"original source bytes\n")
            stage_mock_candidate_repo(source)
            script_in_source = source / "bin/release/build-embedded-candidate.sh"
            (source / "bin/release/verify-embedded-candidate.py").write_text(
                MOCK_VERIFIER_SCRIPT.format(extra_checks=""), encoding="utf-8")
            subprocess.run(["git", "init", "-q"], cwd=source, check=True)
            subprocess.run(["git", "add", "."], cwd=source, check=True)
            subprocess.run(["git", "-c", "user.name=CI Test", "-c", "user.email=ci@example.invalid",
                            "commit", "-qm", "load fixture"], cwd=source, check=True)
            fakebin = base / "fakebin"
            fakebin.mkdir()
            (fakebin / "docker").write_text("""\
#!/usr/bin/env python3
import hashlib, json, os, pathlib, sys, tarfile
args = sys.argv[1:]
state = pathlib.Path(os.environ["FAKE_LOAD_STATE"])
state.mkdir(exist_ok=True)
if args == ["buildx", "version"]:
    print("buildx mocked")
    sys.exit(0)
if args[:2] == ["load", "--input"]:
    with tarfile.open(args[2]) as archive:
        entry = json.loads(archive.extractfile("manifest.json").read())[0]
    config = "sha256:" + entry["Config"].removeprefix("blobs/sha256/")
    if os.environ.get("FAKE_LOAD_DRIFT") == "1":
        config = "sha256:" + ("0" if config[-1] != "0" else "1") * 64
    for tag in entry["RepoTags"]:
        (state / tag.replace(":", "_").replace("/", "_")).write_text(config)
    print("Loaded image: " + entry["RepoTags"][0])
    sys.exit(0)
if args[:4] == ["image", "inspect", "--format", "{{.Id}}"]:
    path = state / args[4].replace(":", "_").replace("/", "_")
    if not path.exists():
        sys.exit(1)
    print(path.read_text())
    sys.exit(0)
assert args[:2] == ["buildx", "build"], args
role = [item.split("=", 1)[1] for item in args
        if item.startswith("io.cpamp.candidate.role=")][0]
output = pathlib.Path([part.removeprefix("dest=") for item in args
                       if item.startswith("type=oci,")
                       for part in item.split(",") if part.startswith("dest=")][0]).parent
config = "sha256:" + hashlib.sha256(("config-" + role).encode()).hexdigest()
layer = "sha256:" + hashlib.sha256(("layer-" + role).encode()).hexdigest()
manifest = json.dumps({"schemaVersion": 2, "config": {"digest": config},
                       "layers": [{"digest": layer}]}).encode()
index = json.dumps({"schemaVersion": 2, "manifests": [
    {"digest": "sha256:" + hashlib.sha256(manifest).hexdigest()}]}).encode()

def write(path, members):
    with tarfile.open(path, "w") as archive:
        for name, content in members.items():
            info = tarfile.TarInfo(name)
            info.size = len(content)
            archive.addfile(info, __import__("io").BytesIO(content))

write(output / (role + ".oci.tar"), {
    "index.json": index,
    "blobs/sha256/" + hashlib.sha256(manifest).hexdigest(): manifest,
})
transport = json.dumps([{"Config": "blobs/" + config.replace(":", "/", 1),
                         "RepoTags": ["cpamp-r1-" + role + ":candidate"],
                         "Layers": ["blobs/" + layer.replace(":", "/", 1)]}]).encode()
write(output / (role + ".docker.tar"), {"manifest.json": transport})
(output / (role + ".digest")).write_text(config)
with open(os.environ["FAKE_LOAD_RECORD"], "a", encoding="utf-8") as log:
    log.write(role + "\\n")
""", encoding="utf-8")
            (fakebin / "docker").chmod(0o755)
            env = dict(os.environ)
            env["PATH"] = str(fakebin) + os.pathsep + env.get("PATH", "")
            env["NODE_PATH"] = str(VERIFIER.parents[2] / "node_modules")
            env["CPAMP_CANDIDATE_LOAD"] = "1"
            env["FAKE_LOAD_STATE"] = str(base / "state")
            env["FAKE_LOAD_RECORD"] = str(base / "loaded.txt")
            output = base / "candidate-output"
            accepted = subprocess.run(["bash", str(script_in_source), str(output)],
                                      cwd=source, env=env, capture_output=True, text=True,
                                      timeout=40)
            self.assertEqual(accepted.returncode, 0, accepted.stdout + "\n" + accepted.stderr)
            for role in ("slim", "full"):
                self.assertIn("LOAD PASS cpamp-r1-" + role + ":candidate", accepted.stdout)
            evidence = json.loads((output / "load-evidence.json").read_text())
            self.assertEqual(set(evidence["images"]), {"slim", "full"})
            for role, record in evidence["images"].items():
                self.assertTrue(record["configBlobMatchesOCI"], record)
                self.assertTrue(record["layerDigestsMatchOCI"], record)
                self.assertEqual(record["imageConfigDigest"], record["imageId"])
                self.assertEqual(record["layers"], 1)
                self.assertEqual(record["transport"],
                                 "docker-format export of the same buildx invocation")
            self.assertEqual((base / "loaded.txt").read_text().split(),
                             ["slim", "full"])
            env["FAKE_LOAD_DRIFT"] = "1"
            drifted = base / "drifted-output"
            mismatch = subprocess.run(["bash", str(script_in_source), str(drifted)],
                                      cwd=source, env=env, capture_output=True, text=True,
                                      timeout=40)
            self.assertNotEqual(mismatch.returncode, 0)
            self.assertIn("loaded image identity differs", mismatch.stderr)
            self.assertFalse((drifted / "load-evidence.json").exists(),
                             "identity drift must not produce load evidence")


class CandidateBuilderSelectionContracts(unittest.TestCase):
    """The OCI exporter needs a container builder; the selection is explicit."""

    def fixture(self, base):
        source = base / "source"
        source.mkdir()
        (source / "Dockerfile").write_bytes(b"FROM scratch\n")
        stage_mock_candidate_repo(source)
        script = source / "bin/release/build-embedded-candidate.sh"
        (source / "bin/release/verify-embedded-candidate.py").write_text(
            MOCK_VERIFIER_SCRIPT.format(extra_checks="""\
for role in ("slim", "full"):
    assert pathlib.Path(a[a.index("--" + role) + 1]).exists()
"""), encoding="utf-8")
        subprocess.run(["git", "init", "-q"], cwd=source, check=True)
        subprocess.run(["git", "add", "."], cwd=source, check=True)
        subprocess.run(["git", "-c", "user.name=CI Test", "-c", "user.email=ci@example.invalid",
                        "commit", "-qm", "builder fixture"], cwd=source, check=True)
        fakebin = base / "fakebin"
        fakebin.mkdir()
        (fakebin / "docker").write_text("""\
#!/usr/bin/env python3
import json, os, pathlib, sys
args = sys.argv[1:]
if args == ["buildx", "version"]:
    print("buildx mocked")
    sys.exit(0)
if args[:2] == ["buildx", "inspect"]:
    if len(args) > 2 and os.environ.get("FAKE_MISSING_BUILDER") == "1":
        print("ERROR: no builder named " + args[2], file=sys.stderr)
        sys.exit(1)
    print("Driver: " + os.environ.get("FAKE_DRIVER", "docker-container"))
    sys.exit(0)
assert args[:2] == ["buildx", "build"], args
builder = args[args.index("--builder") + 1] if "--builder" in args else ""
role = [item.split("=", 1)[1] for item in args
        if item.startswith("io.cpamp.candidate.role=")][0]
for item in args:
    if item.startswith("type=oci,"):
        dest = [part[5:] for part in item.split(",") if part.startswith("dest=")][0]
        pathlib.Path(dest).write_text("fake OCI " + role)
with open(os.environ["FAKE_BUILD_RECORD"], "a", encoding="utf-8") as log:
    log.write(json.dumps({"role": role, "builder": builder}) + "\\n")
""", encoding="utf-8")
        (fakebin / "docker").chmod(0o755)
        env = dict(os.environ)
        env["PATH"] = str(fakebin) + os.pathsep + env.get("PATH", "")
        env["NODE_PATH"] = str(VERIFIER.parents[2] / "node_modules")
        env["FAKE_BUILD_RECORD"] = str(base / "builds.jsonl")
        env.pop("CPAMP_CANDIDATE_LOAD", None)
        env.pop("CPAMP_CANDIDATE_BUILDER", None)
        env.pop("FAKE_MISSING_BUILDER", None)
        return source, script, env

    def test_docker_driver_default_builder_stops_before_building(self):
        with tempfile.TemporaryDirectory(prefix="cpamp-r1-builder-test-") as temp:
            base = Path(temp)
            source, script, env = self.fixture(base)
            env["FAKE_DRIVER"] = "docker"
            result = subprocess.run(["bash", str(script), str(base / "out")], cwd=source,
                                    env=env, capture_output=True, text=True, timeout=25)
            self.assertNotEqual(result.returncode, 0, result.stdout)
            self.assertIn("cannot export an OCI layout", result.stderr)
            self.assertIn("CPAMP_CANDIDATE_BUILDER", result.stderr)
            self.assertFalse((base / "builds.jsonl").exists(),
                             "the docker driver must not start any build")
            self.assertFalse((base / "out" / "candidate-evidence.json").exists())

    def test_container_driver_default_builder_builds_without_a_selection(self):
        with tempfile.TemporaryDirectory(prefix="cpamp-r1-builder-test-") as temp:
            base = Path(temp)
            source, script, env = self.fixture(base)
            env["FAKE_DRIVER"] = "docker-container"
            result = subprocess.run(["bash", str(script), str(base / "out")], cwd=source,
                                    env=env, capture_output=True, text=True, timeout=25)
            self.assertEqual(result.returncode, 0, result.stdout + "\n" + result.stderr)
            events = [json.loads(row) for row in
                      (base / "builds.jsonl").read_text().splitlines()]
            self.assertEqual([row["role"] for row in events], ["slim", "full"])
            self.assertEqual({row["builder"] for row in events}, {""})
            self.assertTrue((base / "out" / "candidate-evidence.json").exists())

    def test_explicit_selection_is_forwarded_to_every_role(self):
        with tempfile.TemporaryDirectory(prefix="cpamp-r1-builder-test-") as temp:
            base = Path(temp)
            source, script, env = self.fixture(base)
            env["FAKE_DRIVER"] = "docker"
            env["CPAMP_CANDIDATE_BUILDER"] = "cpamp-test-builder"
            result = subprocess.run(["bash", str(script), str(base / "out")], cwd=source,
                                    env=env, capture_output=True, text=True, timeout=25)
            self.assertEqual(result.returncode, 0, result.stdout + "\n" + result.stderr)
            events = [json.loads(row) for row in
                      (base / "builds.jsonl").read_text().splitlines()]
            self.assertEqual([row["role"] for row in events], ["slim", "full"])
            self.assertEqual({row["builder"] for row in events}, {"cpamp-test-builder"})

    def test_missing_selected_builder_stops_before_building(self):
        with tempfile.TemporaryDirectory(prefix="cpamp-r1-builder-test-") as temp:
            base = Path(temp)
            source, script, env = self.fixture(base)
            env["CPAMP_CANDIDATE_BUILDER"] = "absent-builder"
            env["FAKE_MISSING_BUILDER"] = "1"
            result = subprocess.run(["bash", str(script), str(base / "out")], cwd=source,
                                    env=env, capture_output=True, text=True, timeout=25)
            self.assertNotEqual(result.returncode, 0, result.stdout)
            self.assertIn("buildx builder not found: absent-builder", result.stderr)
            self.assertFalse((base / "builds.jsonl").exists())


if __name__ == "__main__":
    unittest.main()
