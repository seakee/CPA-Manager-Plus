#!/usr/bin/env python3
"""Offline unit tests; no Docker daemon or registry required."""
import importlib.util
import hashlib
import json
from pathlib import Path
import unittest
from unittest import mock

P = Path(__file__).with_name('check-v2-private-candidate.py')
spec = importlib.util.spec_from_file_location('v2_candidate', P)
audit = importlib.util.module_from_spec(spec)
spec.loader.exec_module(audit)


class AuditTest(unittest.TestCase):
    def test_cpa_license_requires_exact_bytes_and_readonly_regular_file(self):
        from tempfile import TemporaryDirectory
        with TemporaryDirectory() as td:
            license_file = Path(td) / 'LICENSE'
            with self.assertRaisesRegex(ValueError, 'missing regular'):
                audit.check_cpa_license(license_file)
            # Synthetic bytes exercise the boundary; real archive/image equality
            # is verified separately against the pinned official release.
            content = b'fixture license\n'
            license_file.write_bytes(content)
            license_file.chmod(0o444)
            with mock.patch.object(audit, 'CPA_LICENSE_SHA256',
                                   hashlib.sha256(content).hexdigest()):
                audit.check_cpa_license(license_file)
                license_file.chmod(0o644)
                with self.assertRaisesRegex(ValueError, 'read-only'):
                    audit.check_cpa_license(license_file)
            license_file.chmod(0o444)
            with self.assertRaisesRegex(ValueError, 'differs from official'):
                audit.check_cpa_license(license_file)
            link = Path(td) / 'LICENSE-link'
            link.symlink_to(license_file)
            with self.assertRaisesRegex(ValueError, 'missing regular'):
                audit.check_cpa_license(link)

    def test_runtime_audit_rejects_missing_or_replaced_license_and_removes_container(self):
        from tempfile import TemporaryDirectory
        manifest = json.dumps({'schemaVersion': 1, 'engine': 'cpa', 'version': '8.0.55',
                               'artifactId': 'sha256:' + audit.CPA_EXECUTABLE_SHA256['amd64']})
        for missing in (True, False):
            def fake_extract(container, source, dest):
                if source == audit.CPA_LICENSE_PATH:
                    if missing:
                        raise ValueError('missing regular file')
                    dest.write_bytes(b'replaced license')
                    dest.chmod(0o444)
                elif source.endswith('cpa-artifact.json'):
                    dest.write_text(manifest)
                return audit.CPA_EXECUTABLE_SHA256['amd64']
            with self.subTest(missing=missing), TemporaryDirectory() as td, \
                 mock.patch.object(audit, 'inspect_image', return_value={
                     'imageConfigId': 'sha256:' + 'a' * 64, 'architecture': 'amd64'}), \
                 mock.patch.object(audit, 'command', return_value='container-id') as cmd, \
                 mock.patch.object(audit, 'extract_file', side_effect=fake_extract):
                with self.assertRaisesRegex(ValueError, 'missing regular|differs from official'):
                    audit.audit_one('runtime', 'runtime:test', Path(td))
                self.assertEqual(cmd.call_args_list[-1].args[:3], ('docker', 'rm', '-f'))

    def test_artifact_requires_exact_pinned_v8(self):
        for architecture, pinned_sha in audit.CPA_EXECUTABLE_SHA256.items():
            v = {'schemaVersion': 1, 'engine': 'cpa', 'version': '8.0.55',
                 'artifactId': 'sha256:' + pinned_sha}
            with self.subTest(architecture=architecture):
                self.assertEqual(audit.check_artifact(json.dumps(v), architecture), v)
            other = next(arch for arch in audit.CPA_EXECUTABLE_SHA256 if arch != architecture)
            with self.subTest(architecture=architecture, mismatch=other), self.assertRaises(ValueError):
                audit.check_artifact(json.dumps(v), other)
            for field, bad in [('version', '7.3.3'), ('artifactId', 'sha256:deadbeef'),
                               ('engine', 'cpamp'), ('schemaVersion', 2),
                               ('schemaVersion', True), ('schemaVersion', 1.0)]:
                broken = v.copy()
                broken[field] = bad
                with self.subTest(architecture=architecture, field=field), self.assertRaises(ValueError):
                    audit.check_artifact(json.dumps(broken), architecture)

    def test_platform_and_image_id_fail_closed(self):
        value = {'Os': 'linux', 'Architecture': 'amd64', 'Id': 'sha256:' + 'a' * 64,
                 'RepoDigests': ['test/image@sha256:' + 'b' * 64]}
        for architecture in ('amd64', 'arm64'):
            with self.subTest(architecture=architecture), mock.patch.object(
                    audit, 'command', return_value=json.dumps([{**value, 'Architecture': architecture}])):
                r = audit.inspect_image('example:test')
                self.assertEqual(r['imageConfigId'], value['Id'])
                self.assertEqual(r['architecture'], architecture)
        for change in ({'Architecture': 'riscv64'}, {'Os': 'windows'},
                       {'Id': 'sha256:unverified'}, {'Config': {'Volumes': {'/data': {}}}}):
            with self.subTest(change=change), mock.patch.object(audit, 'command',
                       return_value=json.dumps([{**value, **change}])), self.assertRaises(ValueError):
                audit.inspect_image('example:wrong')

    def test_audit_creates_from_inspected_id_not_mutable_tag(self):
        image_id = 'sha256:' + 'a' * 64
        with mock.patch.object(audit, 'inspect_image', return_value={'imageConfigId': image_id, 'architecture': 'amd64'}), \
             mock.patch.object(audit, 'command', return_value='container-id') as cmd, \
             mock.patch.object(audit, 'extract_file', return_value='b' * 64):
            from tempfile import TemporaryDirectory
            with TemporaryDirectory() as td:
                record = audit.audit_one('manager', 'mutable:latest', Path(td))
        self.assertEqual(record['imageConfigId'], image_id)
        self.assertEqual(record['files']['/usr/local/bin/cpa-manager-plus'], 'b' * 64)
        create = [call.args for call in cmd.call_args_list if call.args[:2] == ('docker', 'create')]
        self.assertEqual(len(create), 1)
        self.assertEqual(create[0][-1], image_id)
        self.assertIn('--pull=never', create[0])
        self.assertEqual(create[0][create[0].index('--platform') + 1], 'linux/amd64')
        self.assertNotIn('mutable:latest', create[0])

    def test_arm64_stopped_container_uses_inspected_platform(self):
        image_id = 'sha256:' + 'a' * 64
        with mock.patch.object(audit, 'inspect_image', return_value={
                'imageConfigId': image_id, 'architecture': 'arm64'}), \
             mock.patch.object(audit, 'command', return_value='container-id') as cmd, \
             mock.patch.object(audit, 'extract_file', return_value='b' * 64):
            from tempfile import TemporaryDirectory
            with TemporaryDirectory() as td:
                audit.audit_one('manager', 'example:arm64', Path(td))
        create = [call.args for call in cmd.call_args_list if call.args[:2] == ('docker', 'create')]
        self.assertEqual(len(create), 1)
        self.assertEqual(create[0][create[0].index('--platform') + 1], 'linux/arm64')
        self.assertEqual(create[0][-1], image_id)

    def test_extract_file_rejects_symlink_to_host_file(self):
        from tempfile import TemporaryDirectory
        with TemporaryDirectory() as td:
            base = Path(td)
            host_file = base / 'host-secret'
            host_file.write_bytes(b'not packaged in image')
            destination = base / 'extracted'
            def fake_docker_copy(*args):
                self.assertEqual(args[:2], ('docker', 'cp'))
                destination.symlink_to(host_file)
                return ''
            with mock.patch.object(audit, 'command', side_effect=fake_docker_copy):
                with self.assertRaisesRegex(ValueError, 'missing regular file'):
                    audit.extract_file('stopped-container', '/usr/local/bin/cpa-manager-plus', destination)

    def test_stopped_container_removed_on_cp_failure(self):
        with mock.patch.object(audit, 'inspect_image', return_value={'imageConfigId': 'sha256:' + 'a'*64, 'architecture': 'amd64'}), \
             mock.patch.object(audit, 'command', return_value='123') as cmd, \
             mock.patch.object(audit, 'extract_file', side_effect=ValueError('missing binary')):
            from tempfile import TemporaryDirectory
            with TemporaryDirectory() as td, self.assertRaises(ValueError):
                audit.audit_one('manager', 'example:test', Path(td))
            self.assertEqual(cmd.call_args_list[-1].args[:3], ('docker', 'rm', '-f'))
            self.assertEqual(cmd.call_args_list[0].args[:2], ('docker', 'create'))
            self.assertIn('--pull=never', cmd.call_args_list[0].args)
            self.assertIn('--name', cmd.call_args_list[0].args)


    def test_matching_arm64_triplet_writes_only_partial_evidence(self):
        from tempfile import TemporaryDirectory
        with TemporaryDirectory() as td:
            destination = Path(td) / 'candidate.json'
            argv = ['check-v2-private-candidate.py', '--source-commit', 'a' * 40,
                    '--manager-image', 'manager:v8', '--ingress-image', 'ingress:v8',
                    '--runtime-image', 'runtime:v8', '--output', str(destination)]
            def fake_audit(role, image, tmpdir):
                return {'architecture': 'arm64', 'imageConfigId': 'sha256:' + 'b'*64, 'files': {}}
            with mock.patch.object(audit.sys, 'argv', argv), mock.patch.object(
                    audit, 'audit_one', side_effect=fake_audit) as audited:
                audit.main()
            evidence = json.loads(destination.read_text())
            self.assertEqual(audited.call_count, 3)
            self.assertEqual(evidence['platform'], 'linux/arm64')
            self.assertEqual(evidence['pinnedCPAExecutableSHA256'], audit.CPA_EXECUTABLE_SHA256['arm64'])
            self.assertEqual(evidence['result'], 'PARTIAL_BINARY_IDENTITY_ONLY_NOT_RELEASE_QUALIFIED')

    def test_mixed_architecture_triplet_rejected_before_evidence_written(self):
        from tempfile import TemporaryDirectory
        with TemporaryDirectory() as td:
            destination = Path(td) / 'candidate.json'
            argv = ['check-v2-private-candidate.py', '--source-commit', 'a' * 40,
                    '--manager-image', 'manager:v8', '--ingress-image', 'ingress:v8',
                    '--runtime-image', 'runtime:v8', '--output', str(destination)]
            architectures = {'manager': 'arm64', 'ingress': 'amd64', 'runtime': 'arm64'}
            def fake_audit(role, image, tmpdir):
                return {'architecture': architectures[role], 'imageConfigId': 'sha256:' + 'b'*64, 'files': {}}
            with mock.patch.object(audit.sys, 'argv', argv), mock.patch.object(
                    audit, 'audit_one', side_effect=fake_audit):
                with self.assertRaisesRegex(ValueError, 'matching architecture'):
                    audit.main()
            self.assertFalse(destination.exists())


if __name__ == '__main__':
    unittest.main()
